package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// CustomerSessionCookie is the cookie that carries the guest cart session id.
const CustomerSessionCookie = "shopkeet_session"

// AfterCommit registers fn to run once the request's transaction commits. A
// handler that produces a domain event inside its tx (order.created,
// customers.signup) registers the emit here so subscribers observe committed
// rows — a goroutine launched before commit would miss them. Every RLS-scoped
// request middleware flushes these after tx.Commit succeeds.
func AfterCommit(c *fiber.Ctx, fn func()) {
	fns, _ := c.Locals(postCommitKey).([]func())
	c.Locals(postCommitKey, append(fns, fn))
}

// commitAndFlush commits the request transaction and then runs any
// AfterCommit callbacks, so side effects (event emits) see committed data.
func commitAndFlush(c *fiber.Ctx, tx pgx.Tx, ctx context.Context) error {
	if err := tx.Commit(ctx); err != nil {
		return httperr.ErrInternalServerError
	}
	if fns, ok := c.Locals(postCommitKey).([]func()); ok && len(fns) > 0 {
		c.Locals(postCommitKey, nil)
		for _, fn := range fns {
			fn()
		}
	}
	return nil
}

type postCommitType struct{}

var postCommitKey postCommitType

// TenantCreatedHook runs inside the signup transaction, once RLS is scoped to
// the new tenant, so later phases can seed per-tenant defaults atomically with
// tenant creation (Phase 6 seeds the storefront chrome via content.SeedDefaults).
type TenantCreatedHook func(ctx context.Context, tx pgx.Tx, tenantID string) error

var onTenantCreated TenantCreatedHook

// RegisterTenantCreatedHook lets other packages seed per-tenant defaults without
// auth importing them (they import auth for middleware, so the dependency must
// point the other way). Safe to call before app startup.
func RegisterTenantCreatedHook(h TenantCreatedHook) {
	onTenantCreated = h
}

// --- helpers -----------------------------------------------------------------

// stable returns a copy of s that owns its own bytes. c.Get("X-Tenant-ID") and
// c.Params return strings that alias fasthttp's per-request buffers, which are
// reused after the request completes; any such value stored in an AfterCommit
// event and read later by a goroutine must be copied here first. Live evidence:
// a subscription-delivery span once produced a tenant UUID like
// "gzip480e-...-7917f27099a1" (4 bytes rewritten to "gzip") because the header
// buffer had been reused by a subsequent request.
func stable(s string) string { return strings.Clone(s) }

// isUniqueViolation reports whether err is a Postgres 23505 (unique_violation),
// used by signup to return a clean 409 instead of a 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- stores & accounts --------------------------------------------------------

// storeRow is one store an account can open, read through the RLS-free
// account-scoped tables (migration 0034) because at login time no tenant scope
// exists yet.
type storeRow struct {
	TenantID            string
	Role                string
	Name                string
	Subdomain           string
	OnboardingCompleted bool
	Status              string
}

func storeJSON(s storeRow) fiber.Map {
	return fiber.Map{
		"tenant_id":            s.TenantID,
		"name":                 s.Name,
		"subdomain":            s.Subdomain,
		"role":                 s.Role,
		"status":               s.Status,
		"onboarding_completed": s.OnboardingCompleted,
	}
}

// listStores reads every store for an account, oldest first so a merchant's
// default landing store is stable across logins.
func listStores(ctx context.Context, pool *pgxpool.Pool, accountID string) ([]storeRow, error) {
	rows, err := pool.Query(ctx, `
		SELECT s.tenant_id, s.role, t.name, t.subdomain, t.onboarding_completed, t.status
		FROM merchant_account_stores s
		JOIN tenants t ON t.id = s.tenant_id
		WHERE s.account_id = $1
		ORDER BY t.created_at`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []storeRow
	for rows.Next() {
		var s storeRow
		if err := rows.Scan(&s.TenantID, &s.Role, &s.Name, &s.Subdomain,
			&s.OnboardingCompleted, &s.Status); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// addMembership writes BOTH halves of a membership: the tenant-scoped
// merchant_users row (RLS-protected, read by admin routes) and the
// account-scoped merchant_account_stores row (read by login). They are kept in
// step by construction here and by the FK between them, so store switching can
// never see a store admin routes disagree with.
func addMembership(ctx context.Context, tx pgx.Tx, accountID, tenantID, email, hash, role string) (string, error) {
	// Scope the insert so RLS on merchant_users sees the new tenant (immune even
	// if policies change later).
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return "", err
	}
	var userID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO merchant_users (tenant_id, account_id, email, password_hash, role)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id`, tenantID, accountID, email, hash, role).Scan(&userID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO merchant_account_stores (account_id, tenant_id, role)
		VALUES ($1, $2, $3)`, accountID, tenantID, role); err != nil {
		return "", err
	}
	return userID, nil
}

// provisionalSubdomain mints a unique slug for the store that signup creates
// before the merchant has named anything. It is replaced by the wizard's choice
// of store link, so it only has to be unique and URL-safe.
func provisionalSubdomain() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return "store-" + hex.EncodeToString(b)
}

// --- signup -------------------------------------------------------------------

// signupRequest is the POST /api/v1/auth/signup body. Only the account is asked
// for: the store is created provisionally and named in the one-time wizard that
// follows, so a merchant's first screen is never a store-name field.
type signupRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// SignupHandler creates the account, a provisional store and its owner
// membership in one transaction. With a mailer configured the response is the
// OTP step ({mode:"otp_required"}) and NO token — the JWT only exists after
// the emailed code verifies (docs/05 §login). Without one (no real mail
// provider configured) it falls back to minting the onboarding JWT straight
// away, which is what routes the merchant into the wizard.
func SignupHandler(pool *pgxpool.Pool, secret string, mailer Mailer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req signupRequest
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		if req.Email == "" || req.Password == "" {
			return httperr.C(fiber.StatusBadRequest, "email, password required")
		}

		hash, err := HashPassword(req.Password)
		if err != nil {
			return httperr.ErrInternalServerError
		}

		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)

		var accountID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO merchant_accounts (email, password_hash)
			VALUES ($1, $2)
			RETURNING id`, req.Email, hash).Scan(&accountID); err != nil {
			if isUniqueViolation(err) {
				return httperr.C(fiber.StatusConflict, "an account with this email already exists")
			}
			return httperr.ErrInternalServerError
		}

		subdomain := provisionalSubdomain()
		var tenantID string
		// onboarding_completed defaults to true for existing stores; signup
		// explicitly opts into the wizard.
		if err := tx.QueryRow(ctx, `
			INSERT INTO tenants (name, subdomain, onboarding_completed)
			VALUES ($1, $2, false)
			RETURNING id`, "My store", subdomain).Scan(&tenantID); err != nil {
			if isUniqueViolation(err) {
				return httperr.ErrInternalServerError
			}
			return httperr.ErrInternalServerError
		}

		userID, err := addMembership(ctx, tx, accountID, tenantID, req.Email, hash, "owner")
		if err != nil {
			if isUniqueViolation(err) {
				return httperr.C(fiber.StatusConflict, "email belongs to this tenant")
			}
			return httperr.ErrInternalServerError
		}

		if onTenantCreated != nil {
			if err := onTenantCreated(c.Context(), tx, tenantID); err != nil {
				return httperr.ErrInternalServerError
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return httperr.ErrInternalServerError
		}

		if mailer != nil {
			// The account exists but is not authenticated: issue the code
			// before answering. A send failure still returns otp_required (log
			// it, don't 500) — a 500 here would strand the merchant retrying
			// into a 409 conflict, while the resend button on the OTP page is
			// the proper retry path and surfaces real errors. A code-row
			// failure likewise degrades to "log in again", which re-issues.
			if code, err := issueCode(ctx, pool, accountID, req.Email, otpTTL); err != nil {
				log.Printf("[auth] otp issue failed for signup %s: %v", req.Email, err)
			} else if err := mailer.SendOTP(ctx, req.Email, code); err != nil {
				log.Printf("[auth] otp send failed for signup %s: %v", req.Email, err)
			}
			return c.Status(fiber.StatusCreated).JSON(fiber.Map{
				"mode": "otp_required", "email": req.Email, "method": "email",
			})
		}

		onboarding := false
		token, err := SignMerchant(secret, tenantID, userID, accountID, "owner", &onboarding, 24*time.Hour)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"token": token,
			"user":  fiber.Map{"id": accountID, "email": req.Email, "role": "owner"},
			"store": fiber.Map{
				"tenant_id": tenantID, "name": "My store", "subdomain": subdomain,
				"role": "owner", "status": "active", "onboarding_completed": false,
			},
			"onboarding_completed": false,
			"expires":              time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	}
}

// --- login -------------------------------------------------------------------

// loginRequest is the POST /api/v1/auth/login body: an account identity only.
// A merchant can own several stores, so the login never asks which one — it
// returns the whole set and lets the merchant pick.
type loginRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

// LoginHandler authenticates the account (email + password). With a mailer
// configured that is only HALF the gate: a correct password issues the OTP and
// returns {mode:"otp_required"} with no token — the session payload (shared
// with OTP verify and reset via sessionPayload) comes back only after the
// emailed code verifies. A browser already trusted (valid X-Otp-Bypass ticket
// for this account, minted by a past verification) skips the code and gets the
// payload straight away. Without a mailer it returns the payload directly:
// one store → the tenant JWT; several → only a short-lived store_pick ticket,
// so a store cannot be chosen without a membership row saying the account may
// act on it.
func LoginHandler(pool *pgxpool.Pool, secret string, mailer Mailer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req loginRequest
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))

		ctx := c.Context()
		// merchant_accounts is account-scoped and RLS-free by design, so this
		// resolves before any tenant exists — the same reasoning that lets
		// tenants be looked up by subdomain before login.
		var accountID, hash string
		if err := pool.QueryRow(ctx, `
			SELECT id, password_hash FROM merchant_accounts WHERE lower(email) = lower($1)`,
			req.Email).Scan(&accountID, &hash); err != nil {
			if err == pgx.ErrNoRows {
				return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
			}
			return httperr.ErrInternalServerError
		}
		if hash == "" || !CheckPassword(req.Password, hash) {
			// Identical response for an unknown email and a wrong password, so
			// login cannot be used to enumerate accounts.
			return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
		}

		if mailer != nil {
			// Password good, second factor normally pending. A browser that
			// proved the mailbox in the past carries a trusted-device ticket
			// which the web app forwards as X-Otp-Bypass: with a valid one for
			// THIS account, the interim code step is skipped. Any tamper,
			// expiry, or password change falls through to the full gate.
			if bypassAccount, ok := ParseOTPBypass(secret, hash, c.Get("X-Otp-Bypass")); !ok || bypassAccount != accountID {
				// Same stance as signup: a send failure is logged, not
				// surfaced — otp_required still goes back so the OTP page's
				// Resend button owns the retry UX.
				if code, err := issueCode(ctx, pool, accountID, req.Email, otpTTL); err != nil {
					return httperr.ErrInternalServerError
				} else if err := mailer.SendOTP(ctx, req.Email, code); err != nil {
					log.Printf("[auth] otp send failed for login %s: %v", req.Email, err)
				}
				return c.JSON(fiber.Map{
					"mode": "otp_required", "email": req.Email, "method": "email",
				})
			}
		}

		payload, err := sessionPayload(ctx, pool, secret, accountID, req.Email)
		if err != nil {
			return err
		}
		return c.JSON(payload)
	}
}

func storeListJSON(stores []storeRow) []fiber.Map {
	out := make([]fiber.Map, 0, len(stores))
	for _, s := range stores {
		out = append(out, storeJSON(s))
	}
	return out
}

// membershipUserID resolves the tenant-scoped user id for an account's membership
// in a store. Needed because the JWT's user_id must be the merchant_users row id
// (admin routes key off it), not the account id.
func membershipUserID(ctx context.Context, pool *pgxpool.Pool, accountID, tenantID string) (string, error) {
	var userID string
	// merchant_users is FORCE RLS, so read it through the account-scoped mapping
	// plus a per-tenant transaction — same shape the old login used.
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
		return "", err
	}
	if err := tx.QueryRow(ctx,
		`SELECT id FROM merchant_users WHERE account_id = $1 AND tenant_id = $2`,
		accountID, tenantID).Scan(&userID); err != nil {
		return "", err
	}
	return userID, tx.Commit(ctx)
}

// --- store selection ----------------------------------------------------------

// parseStoreToken authorizes the store-list and store-select endpoints from
// either a store_pick ticket or a merchant token.
func parseStoreToken(c *fiber.Ctx, secret string) (string, int, string) {
	auth := c.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") {
		return "", fiber.StatusUnauthorized, "missing bearer token"
	}
	accountID, ok := ParseStorePickAuthorizes(secret, strings.TrimPrefix(auth, "Bearer "))
	if !ok {
		return "", fiber.StatusUnauthorized, "store selection token rejected"
	}
	return accountID, 0, ""
}

// StoresHandler lists the stores an authenticated account can open. Requires a
// store_pick ticket or a merchant token, so it is unreachable without either a
// password check or an existing session.
func StoresHandler(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		accountID, status, msg := parseStoreToken(c, secret)
		if accountID == "" {
			return httperr.C(status, msg)
		}
		stores, err := listStores(c.Context(), pool, accountID)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if len(stores) == 0 {
			return httperr.C(fiber.StatusForbidden, "this account has no store yet")
		}
		return c.JSON(fiber.Map{"stores": storeListJSON(stores)})
	}
}

type selectStoreRequest struct {
	TenantID string `json:"tenant_id"`
}

// SelectStoreHandler exchanges a store_pick ticket (or an existing merchant
// token, for switching stores) for a tenant-scoped session. Membership is
// re-checked against the account, so a merchant can only ever open a store they
// actually belong to — the ticket alone grants nothing.
func SelectStoreHandler(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		accountID, status, msg := parseStoreToken(c, secret)
		if accountID == "" {
			return httperr.C(status, msg)
		}
		var req selectStoreRequest
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		if req.TenantID == "" {
			return httperr.C(fiber.StatusBadRequest, "tenant_id required")
		}

		ctx := c.Context()
		var store storeRow
		err := pool.QueryRow(ctx, `
			SELECT s.tenant_id, s.role, t.name, t.subdomain, t.onboarding_completed, t.status
			FROM merchant_account_stores s
			JOIN tenants t ON t.id = s.tenant_id
			WHERE s.account_id = $1 AND s.tenant_id = $2`,
			accountID, req.TenantID).Scan(&store.TenantID, &store.Role, &store.Name,
			&store.Subdomain, &store.OnboardingCompleted, &store.Status)
		if err != nil {
			if err == pgx.ErrNoRows {
				return httperr.C(fiber.StatusForbidden, "no access to this store")
			}
			return httperr.ErrInternalServerError
		}
		if store.Status == "suspended" {
			return httperr.C(fiber.StatusForbidden, "this store is suspended")
		}

		userID, err := membershipUserID(ctx, pool, accountID, store.TenantID)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		onboarding := store.OnboardingCompleted
		token, err := SignMerchant(secret, store.TenantID, userID, accountID, store.Role, &onboarding, 24*time.Hour)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		return c.JSON(fiber.Map{
			"token":                token,
			"user":                 fiber.Map{"id": accountID, "role": store.Role},
			"store":                storeJSON(store),
			"onboarding_completed": store.OnboardingCompleted,
			"expires":              time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	}
}

// --- middleware + routes -------------------------------------------------------

// parseMerchant validates an Authorization header that must be a merchant token.
// Any other scope — customer (Phase 11) or affiliate (Phase 27) — is rejected
// here so a storefront shopper or a partner can never reach admin routes.
func parseMerchant(c *fiber.Ctx, secret string) (*Claims, int, string) {
	h := c.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, fiber.StatusUnauthorized, "missing bearer token"
	}
	claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
	if err != nil {
		return nil, fiber.StatusUnauthorized, "invalid token"
	}
	if claims.Scope != "merchant" {
		return nil, fiber.StatusForbidden, "non-merchant token not allowed here"
	}
	return claims, 0, ""
}

// TenantMW validates a Bearer JWT (merchant scope — a customer token is
// refused, Phase 11) and, on success, opens a real DB transaction with
// app.current_tenant SET LOCAL so RLS scopes every query to that tenant for the
// rest of the request. The tx is stored in c.Locals("tx") for handlers to use;
// TenantMW commits it automatically when the handler chain completes without
// error.
func TenantMW(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, status, msg := parseMerchant(c, secret)
		if claims == nil {
			return httperr.C(status, msg)
		}

		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)

		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", claims.TenantID); err != nil {
			return httperr.ErrInternalServerError
		}

		c.Locals("tx", tx)
		c.Locals("tenant_id", stable(claims.TenantID))
		c.Locals("user_id", claims.UserID)
		c.Locals("role", claims.Role)

		if err := c.Next(); err != nil {
			return err
		}
		return commitAndFlush(c, tx, ctx)
	}
}

// publicTenantMW opens the RLS-scoped request transaction for a public
// storefront request. Tenant resolution happens in Next.js middleware from the
// hostname (docs/03-architecture.md §2) and is passed here as X-Tenant-ID;
// this middleware validates the id and SET LOCALs app.current_tenant on the
// request transaction exactly like TenantMW, but without requiring a JWT.
// Public storefront endpoints mount this. Public-or-admin endpoints mount
// PublicOrAdminMW instead.
func publicTenantMW(pool *pgxpool.Pool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tid := stable(c.Get("X-Tenant-ID"))
		if tid == "" {
			return httperr.C(fiber.StatusBadRequest, "missing X-Tenant-ID header")
		}

		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)

		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			return httperr.ErrInternalServerError
		}

		c.Locals("tx", tx)
		c.Locals("tenant_id", tid)
		c.Locals("admin", false)
		if err := c.Next(); err != nil {
			return err
		}
		return commitAndFlush(c, tx, ctx)
	}
}

// PublicTenantMW is publicTenantMW as an exported constructor (storefront-only
// routes).
func PublicTenantMW(pool *pgxpool.Pool) fiber.Handler { return publicTenantMW(pool) }

// PublicOrAdminMW serves routes that are public (active-only listings) but
// also admin-visible (e.g. GET /products/:id — draft/archived included for the
// merchant, active-only for shoppers). It resolves the tenant either from a
// valid Bearer JWT (admin view) or from X-Tenant-ID (public view) and stores
// whether admin in c.Locals("admin").
func PublicOrAdminMW(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		claims, status, msg := parseMerchantWithOptional(c, secret)
		if status > 0 {
			return httperr.C(status, msg)
		}
		if claims == nil {
			return publicTenantMW(pool)(c)
		}

		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", claims.TenantID); err != nil {
			return httperr.ErrInternalServerError
		}
		c.Locals("tx", tx)
		c.Locals("tenant_id", stable(claims.TenantID))
		c.Locals("user_id", claims.UserID)
		c.Locals("role", claims.Role)
		c.Locals("admin", true)

		if err := c.Next(); err != nil {
			return err
		}
		return commitAndFlush(c, tx, ctx)
	}
}

// MerchantOrCustomerMW serves merchant-or-customer endpoints (Phase 15:
// POST /orders/:id/returns) on a single route. A valid merchant Bearer token
// opens the TenantMW identity path (actor=merchant); a customer Bearer token
// or no token at all opens the RLS-scoped request transaction from the token's
// tenant or the X-Tenant-ID header (actor=customer) — there the handler's
// order lookup by phone/email is the real gate, exactly like GET /orders/:id.
// Invalid Bearer tokens fail closed with 401 so the customer path can't be
// bypassed with a bad token.
func MerchantOrCustomerMW(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)

		actor := "customer"
		tid := stable(c.Get("X-Tenant-ID"))
		if h := c.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
			claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				return httperr.C(fiber.StatusUnauthorized, "invalid token")
			}
			switch claims.Scope {
			case "merchant":
				actor = "merchant"
				tid = claims.TenantID
				c.Locals("user_id", claims.UserID)
				c.Locals("role", claims.Role)
			case "customer":
				tid = claims.TenantID
			default:
				return httperr.C(fiber.StatusForbidden, "unknown token scope")
			}
		}
		if tid == "" {
			return httperr.C(fiber.StatusBadRequest, "missing X-Tenant-ID header")
		}
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			return httperr.ErrInternalServerError
		}

		c.Locals("tx", tx)
		c.Locals("tenant_id", tid)
		c.Locals("actor", actor)
		if err := c.Next(); err != nil {
			return err
		}
		return commitAndFlush(c, tx, ctx)
	}
}

// parseMerchantWithOptional resolves a merchant token if a Bearer header is
// present (invalid/customer tokens are still refused — fail closed), and
// returns nil claims when there is no Authorization header at all.
func parseMerchantWithOptional(c *fiber.Ctx, secret string) (*Claims, int, string) {
	if !strings.HasPrefix(c.Get("Authorization"), "Bearer ") {
		return nil, 0, ""
	}
	return parseMerchant(c, secret)
}

// CustomerMW serves guest-storefront routes (Phase 4 cart). It resolves the
// tenant from X-Tenant-ID exactly like PublicTenantMW and additionally ensures
// a customer_session exists for guest carts:
//
//   - from the shopkeet_session cookie, or the X-Customer-Session header;
//   - if neither is present it mints a new UUID, sets an HttpOnly cookie, and
//     echoes it back in the X-Customer-Session response header so non-cookie
//     callers (the Next.js storefront) can persist it client-side.
//
// The session id is stored in c.Locals("customer_session"). RLS still scopes
// rows by tenant; customer_session scoping happens in the cart queries.
func CustomerMW(pool *pgxpool.Pool) fiber.Handler {
	return func(c *fiber.Ctx) error {
		tid := stable(c.Get("X-Tenant-ID"))
		if tid == "" {
			return httperr.C(fiber.StatusBadRequest, "missing X-Tenant-ID header")
		}

		session := c.Cookies(CustomerSessionCookie)
		if h := c.Get("X-Customer-Session"); h != "" {
			session = h
		}
		if session == "" || strings.TrimSpace(session) == "" {
			session = uuid.NewString()
			c.Cookie(&fiber.Cookie{
				Name:     CustomerSessionCookie,
				Value:    session,
				Path:     "/",
				HTTPOnly: true,
				SameSite: "lax",
			})
		}
		c.Set("X-Customer-Session", session)

		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			return httperr.ErrInternalServerError
		}

		c.Locals("tx", tx)
		c.Locals("tenant_id", tid)
		c.Locals("customer_session", session)
		if err := c.Next(); err != nil {
			return err
		}
		return commitAndFlush(c, tx, ctx)
	}
}

// CustomerAuthMW guards Phase 11 customer-account routes. It requires a
// customer-scoped JWT (scope="customer", signed via SignCustomer) and opens the
// RLS-scoped request transaction pinned to the token's tenant — the same shape
// as TenantMW, but the identity is c.Locals("customer_id"), never user_id/role.
func CustomerAuthMW(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		h := c.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			return httperr.C(fiber.StatusUnauthorized, "missing bearer token")
		}
		claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			return httperr.C(fiber.StatusUnauthorized, "invalid token")
		}
		if claims.Scope != "customer" {
			return httperr.C(fiber.StatusForbidden, "merchant token not allowed here")
		}
		if claims.CustomerID == "" {
			return httperr.C(fiber.StatusForbidden, "token carries no customer identity")
		}

		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", claims.TenantID); err != nil {
			return httperr.ErrInternalServerError
		}

		c.Locals("tx", tx)
		c.Locals("tenant_id", stable(claims.TenantID))
		c.Locals("customer_id", claims.CustomerID)
		if err := c.Next(); err != nil {
			return err
		}
		return commitAndFlush(c, tx, ctx)
	}
}

// AffiliateAuthMW guards Phase 27 affiliate-account routes. It requires an
// affiliate-scoped JWT (scope="affiliate", signed via SignAffiliate) and opens
// the RLS-scoped request transaction pinned to the token's tenant — the same
// shape as TenantMW/CustomerAuthMW, but the identity is c.Locals("affiliate_id"),
// never user_id/role or customer_id. Merchant and customer tokens are refused
// here.
func AffiliateAuthMW(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		h := c.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			return httperr.C(fiber.StatusUnauthorized, "missing bearer token")
		}
		claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			return httperr.C(fiber.StatusUnauthorized, "invalid token")
		}
		if claims.Scope != "affiliate" {
			return httperr.C(fiber.StatusForbidden, "non-affiliate token not allowed here")
		}
		if claims.AffiliateID == "" {
			return httperr.C(fiber.StatusForbidden, "token carries no affiliate identity")
		}

		ctx := c.Context()
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", claims.TenantID); err != nil {
			return httperr.ErrInternalServerError
		}

		c.Locals("tx", tx)
		c.Locals("tenant_id", stable(claims.TenantID))
		c.Locals("affiliate_id", claims.AffiliateID)
		if err := c.Next(); err != nil {
			return err
		}
		return commitAndFlush(c, tx, ctx)
	}
}

// CustomerOrGuestMW serves checkout: a signed-in customer (customer JWT) gets
// their tenant from the token and their customer_id attached to the order;
// a guest falls back to the regular CustomerMW guest-session path, leaving
// customer_id NULL (Phase 11). Either way a cart is resolved by guest session,
// so an account holder's cart continues to work before and after login.
func CustomerOrGuestMW(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		h := c.Get("Authorization")
		if strings.HasPrefix(h, "Bearer ") {
			claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				return httperr.C(fiber.StatusUnauthorized, "invalid token")
			}
			if claims.Scope != "customer" {
				return httperr.C(fiber.StatusForbidden, "merchant token not allowed here")
			}

			// Guest-session resolution identical to CustomerMW (a shopper keeps
			// the same cart across login), plus tenant + identity from the token.
			session := c.Cookies(CustomerSessionCookie)
			if hs := c.Get("X-Customer-Session"); hs != "" {
				session = hs
			}
			if session == "" || strings.TrimSpace(session) == "" {
				session = uuid.NewString()
				c.Cookie(&fiber.Cookie{
					Name: CustomerSessionCookie, Value: session, Path: "/",
					HTTPOnly: true, SameSite: "lax",
				})
			}
			c.Set("X-Customer-Session", session)

			ctx := c.Context()
			tx, err := pool.Begin(ctx)
			if err != nil {
				return httperr.ErrInternalServerError
			}
			defer tx.Rollback(ctx)
			if _, err := tx.Exec(ctx,
				"SELECT set_config('app.current_tenant', $1, true)", claims.TenantID); err != nil {
				return httperr.ErrInternalServerError
			}
			c.Locals("tx", tx)
			c.Locals("tenant_id", stable(claims.TenantID))
			c.Locals("customer_session", session)
			c.Locals("customer_id", claims.CustomerID)
			if err := c.Next(); err != nil {
				return err
			}
			return commitAndFlush(c, tx, ctx)
		}
		return CustomerMW(pool)(c)
	}
}

// RegisterRoutes mounts the auth surface onto an existing router that already
// carries the /api/v1 prefix (so later phases can share the group):
//
//	POST /api/v1/auth/signup          (public, rate-limited)
//	POST /api/v1/auth/login           (public, rate-limited)
//	POST /api/v1/auth/otp/send        (public, rate-limited)
//	POST /api/v1/auth/otp/verify      (public, rate-limited)
//	POST /api/v1/auth/forgot-password (public, rate-limited)
//	POST /api/v1/auth/reset-password  (public, rate-limited)
//	GET  /api/v1/auth/stores          (store_pick ticket or merchant token)
//	POST /api/v1/auth/select-store    (store_pick ticket or merchant token)
//
// limiter applies per-route Redis limits (docs/08-hardening… §14): login
// brute-force at 5/15min per IP+email, signup at 10/hour per IP, plus the
// OTP/reset budgets in verification.go — resend/verify/forgot/reset all
// keyed IP+email where the body carries one, so a guesser rotating IPs still
// trips the per-email bucket. A nil limiter disables limiting entirely. The
// two store endpoints sit behind a verified token rather than a limiter —
// they cannot be called without one, and a merchant switching stores should
// not be rate-limited like a login.
//
// mailer gates every verification endpoint: nil (no real mail provider
// configured) leaves OTP/reset disabled and signup/login issue sessions
// directly, so a box that cannot send mail never locks its users out.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, limiter *ratelimit.Limiter, mailer Mailer) {
	g := router.Group("/auth")
	g.Post("/signup",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/signup",
			Limit:   10,
			Window:  time.Hour,
			KeyFunc: ratelimit.ByIP(),
		}),
		SignupHandler(pool, secret, mailer))
	g.Post("/login",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/login",
			Limit:   5,
			Window:  15 * time.Minute,
			KeyFunc: ratelimit.ByIPAndBodyField("email"),
		}),
		LoginHandler(pool, secret, mailer))
	g.Post("/otp/send",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/otp/send",
			Limit:   5,
			Window:  15 * time.Minute,
			KeyFunc: ratelimit.ByIPAndBodyField("email"),
		}),
		OTPSendHandler(pool, mailer))
	g.Post("/otp/verify",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/otp/verify",
			Limit:   15,
			Window:  15 * time.Minute,
			KeyFunc: ratelimit.ByIPAndBodyField("email"),
		}),
		OTPVerifyHandler(pool, secret, mailer))
	g.Post("/forgot-password",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/forgot-password",
			Limit:   5,
			Window:  time.Hour,
			KeyFunc: ratelimit.ByIPAndBodyField("email"),
		}),
		ForgotPasswordHandler(pool, mailer))
	g.Post("/reset-password",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/reset-password",
			Limit:   10,
			Window:  15 * time.Minute,
			KeyFunc: ratelimit.ByIP(),
		}),
		ResetPasswordHandler(pool, secret))
	g.Get("/stores", StoresHandler(pool, secret))
	g.Post("/select-store", SelectStoreHandler(pool, secret))
}

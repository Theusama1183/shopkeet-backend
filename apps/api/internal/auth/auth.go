package auth

import (
	"context"
	"errors"
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

// isUniqueViolation reports whether err is a Postgres 23505 (unique_violation),
// used by signup to return a clean 409 instead of a 500.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- signup -------------------------------------------------------------------

// signupRequest is the POST /api/v1/auth/signup body.
type signupRequest struct {
	Name      string `json:"name"`
	Subdomain string `json:"subdomain"`
	Email     string `json:"email"`
	Password  string `json:"password"`
}

// SignupHandler creates a tenant + its owner in one transaction (so the tenant
// and owner commit atomically), then mints a tenant-scoped JWT.
func SignupHandler(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req signupRequest
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		if req.Name == "" || req.Subdomain == "" || req.Email == "" || req.Password == "" {
			return httperr.C(fiber.StatusBadRequest, "name, subdomain, email, password required")
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

		var tenantID, userID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO tenants (name, subdomain)
			VALUES ($1, $2)
			RETURNING id`, req.Name, req.Subdomain).Scan(&tenantID); err != nil {
			if isUniqueViolation(err) {
				return httperr.C(fiber.StatusConflict, "subdomain taken")
			}
			return httperr.ErrInternalServerError
		}

		// Scope the owner insert so hypothetical RLS on merchant_users sees the
		// new tenant (immune even if policies are added later).
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
			return httperr.ErrInternalServerError
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO merchant_users (tenant_id, email, password_hash, role)
			VALUES ($1, $2, $3, 'owner')
			RETURNING id`, tenantID, req.Email, hash).Scan(&userID); err != nil {
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

		token, err := Sign(secret, tenantID, userID, "owner", 24*time.Hour)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		return c.Status(fiber.StatusCreated).JSON(fiber.Map{
			"token":   token,
			"user":    fiber.Map{"id": userID, "email": req.Email, "role": "owner"},
			"tenant":  fiber.Map{"id": tenantID, "name": req.Name, "subdomain": req.Subdomain},
			"expires": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	}
}

// --- login -------------------------------------------------------------------

// loginRequest is the POST /api/v1/auth/login body.
type loginRequest struct {
	Subdomain string `json:"subdomain"`
	Email     string `json:"email"`
	Password  string `json:"password"`
}

// LoginHandler verifies subdomain+email+password and returns a tenant JWT.
func LoginHandler(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req loginRequest
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}

		ctx := c.Context()

		// Step 1 — resolve the tenant by subdomain. tenants has no RLS (subdomain
		// lookup is public by design), so this succeeds on a plain pool query
		// before any tenant scope is established.
		var tenantID string
		if err := pool.QueryRow(ctx,
			`SELECT id FROM tenants WHERE subdomain = $1`, req.Subdomain).Scan(&tenantID); err != nil {
			if err == pgx.ErrNoRows {
				return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
			}
			return httperr.ErrInternalServerError
		}

		// Step 2 — verify credentials inside the tenant's RLS scope. merchant_users
		// is FORCE RLS, so the lookup runs on a request transaction with
		// app.current_tenant pinned to the tenant resolved above.
		var userID, role, hash string
		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
			return httperr.ErrInternalServerError
		}
		err = tx.QueryRow(ctx, `
			SELECT u.id, u.role, u.password_hash
			FROM merchant_users u
			WHERE u.tenant_id = $1 AND u.email = $2`, tenantID, req.Email).
			Scan(&userID, &role, &hash)
		if err == pgx.ErrNoRows {
			return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
		}
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if hash == "" || !CheckPassword(req.Password, hash) {
			return httperr.C(fiber.StatusUnauthorized, "invalid credentials")
		}

		token, err := Sign(secret, tenantID, userID, role, 24*time.Hour)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		return c.JSON(fiber.Map{
			"token":   token,
			"user":    fiber.Map{"id": userID, "email": req.Email, "role": role},
			"expires": time.Now().Add(24 * time.Hour).Format(time.RFC3339),
		})
	}
}

// --- middleware + routes -------------------------------------------------------

// parseMerchant validates an Authorization header that must be a merchant token.
// A customer-scoped JWT (Phase 11) is rejected here so a storefront shopper can
// never reach admin routes.
func parseMerchant(c *fiber.Ctx, secret string) (*Claims, int, string) {
	h := c.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return nil, fiber.StatusUnauthorized, "missing bearer token"
	}
	claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
	if err != nil {
		return nil, fiber.StatusUnauthorized, "invalid token"
	}
	if claims.Scope == "customer" {
		return nil, fiber.StatusForbidden, "customer token not allowed here"
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
		c.Locals("tenant_id", claims.TenantID)
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
		tid := c.Get("X-Tenant-ID")
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
		c.Locals("tenant_id", claims.TenantID)
		c.Locals("user_id", claims.UserID)
		c.Locals("role", claims.Role)
		c.Locals("admin", true)

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
		tid := c.Get("X-Tenant-ID")
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
		c.Locals("tenant_id", claims.TenantID)
		c.Locals("customer_id", claims.CustomerID)
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
			c.Locals("tenant_id", claims.TenantID)
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

// RegisterRoutes mounts the Phase 1 auth surface onto an existing router that
// already carries the /api/v1 prefix (so later phases can share the group):
//
//	POST /api/v1/auth/signup   (public, rate-limited — Phase 14)
//	POST /api/v1/auth/login    (public, rate-limited — Phase 14)
//
// limiter applies per-route Redis limits: login brute-force at 5/15min per
// IP+email, signup at 10/hour per IP (docs/08-hardening… §14). A nil limiter
// disables limiting entirely.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, limiter *ratelimit.Limiter) {
	g := router.Group("/auth")
	g.Post("/signup",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/signup",
			Limit:   10,
			Window:  time.Hour,
			KeyFunc: ratelimit.ByIP(),
		}),
		SignupHandler(pool, secret))
	g.Post("/login",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /auth/login",
			Limit:   5,
			Window:  15 * time.Minute,
			KeyFunc: ratelimit.ByIPAndBodyField("email"),
		}),
		LoginHandler(pool, secret))
}

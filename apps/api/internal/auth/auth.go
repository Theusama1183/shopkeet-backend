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
)

// CustomerSessionCookie is the cookie that carries the guest cart session id.
const CustomerSessionCookie = "shopkeet_session"

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
		var tenantID, userID, role, hash string
		err := pool.QueryRow(ctx, `
			SELECT t.id, u.id, u.role, u.password_hash
			FROM tenants t
			JOIN merchant_users u ON u.tenant_id = t.id
			WHERE t.subdomain = $1 AND u.email = $2`, req.Subdomain, req.Email).
			Scan(&tenantID, &userID, &role, &hash)
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

// TenantMW validates a Bearer JWTcase and, on success, opens a real DB
// transaction with app.current_tenant SET LOCAL so RLS scopes every query to
// that tenant for the rest of the request. The tx is stored in c.Locals("tx")
// for handlers to use; TenantMW commits it automatically when the handler
// chain completes without error.
func TenantMW(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		h := c.Get("Authorization")
		if !strings.HasPrefix(h, "Bearer ") {
			return httperr.C(fiber.StatusUnauthorized, "missing bearer token")
		}
		claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
		if err != nil {
			return httperr.C(fiber.StatusUnauthorized, "invalid token")
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
		return tx.Commit(ctx)
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
		return tx.Commit(ctx)
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
		h := c.Get("Authorization")
		if strings.HasPrefix(h, "Bearer ") {
			claims, err := Parse(secret, strings.TrimPrefix(h, "Bearer "))
			if err != nil {
				return httperr.C(fiber.StatusUnauthorized, "invalid token")
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
			return tx.Commit(ctx)
		}
		return publicTenantMW(pool)(c)
	}
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
		return tx.Commit(ctx)
	}
}

// RegisterRoutes mounts the Phase 1 auth surface onto an existing router that
// already carries the /api/v1 prefix (so later phases can share the group):
//
//	POST /api/v1/auth/signup   (public)
//	POST /api/v1/auth/login    (public)
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string) {
	g := router.Group("/auth")
	g.Post("/signup", SignupHandler(pool, secret))
	g.Post("/login", LoginHandler(pool, secret))
}

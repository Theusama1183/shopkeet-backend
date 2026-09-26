package customers

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// RegisterRoutes mounts the Phase 11 customer-accounts surface on /api/v1:
//
//	POST /customers/signup           (Public — X-Tenant-ID)
//	POST /customers/login            (Public — X-Tenant-ID)
//	GET  /customers/me               (Customer JWT)
//	GET  /customers/me/orders        (Customer JWT)
//	GET/POST /customers/me/addresses (Customer JWT)
//	PATCH/DELETE /customers/me/addresses/:id (Customer JWT)
//
// signup/login resolve the storefront tenant from X-Tenant-ID (the frontend's
// middleware passes it, as for every public endpoint). The /me group requires a
// customer-scoped JWT — a merchant token is refused by CustomerAuthMW.
// POST /customers/signup is idempotency-guarded (Phase 14) so a retry doesn't
// mint a second account; signup/login are rate-limited (Phase 14).
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service, limiter *ratelimit.Limiter) {
	router.Post("/customers/signup",
		auth.PublicTenantMW(pool),
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /customers/signup",
			Limit:   10,
			Window:  time.Hour,
			KeyFunc: ratelimit.ByIP(),
		}),
		idempotency.Middleware("POST /customers/signup"),
		svc.Signup)
	router.Post("/customers/login",
		auth.PublicTenantMW(pool),
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /customers/login",
			Limit:   5,
			Window:  15 * time.Minute,
			KeyFunc: ratelimit.ByIPAndBodyField("email"),
		}),
		svc.Login)

	me := router.Group("/customers/me", auth.CustomerAuthMW(pool, secret))
	me.Get("/", svc.Me)
	me.Get("/orders", svc.MyOrders)
	me.Get("/addresses", svc.ListAddresses)
	me.Post("/addresses", svc.CreateAddress)
	me.Patch("/addresses/:id", svc.UpdateAddress)
	me.Delete("/addresses/:id", svc.DeleteAddress)
}

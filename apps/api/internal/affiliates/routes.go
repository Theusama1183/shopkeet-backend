package affiliates

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// RegisterRoutes mounts the Phase 27 affiliate surface on /api/v1:
//
//	POST /affiliates/apply            (Public — X-Tenant-ID)
//	POST /affiliates/login            (Public — subdomain + email + password)
//	GET  /affiliates                  (Admin)
//	PATCH /affiliates/:id/status      (Admin — approve/reject/suspend + optional commission_percent)
//	GET  /affiliates/me/dashboard     (Affiliate JWT)
//	GET  /affiliates/me/commissions   (Affiliate JWT)
//	POST /affiliates/me/payout-request (Affiliate JWT)
//	PATCH /affiliate-payouts/:id       (Admin — mark paid)
//
// Affiliates hold a third JWT scope; the /me group uses AffiliateAuthMW and the
// admin routes use TenantMW (which refuses affiliate tokens). apply is
// public+rate-limited and idempotency-guarded like customer signup so a retry
// doesn't mint a second pending application; login is rate-limited like
// merchant/customer login.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service,
	limiter *ratelimit.Limiter) {

	router.Post("/affiliates/apply",
		auth.PublicTenantMW(pool),
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /affiliates/apply",
			Limit:   10,
			Window:  time.Hour,
			KeyFunc: ratelimit.ByIP(),
		}),
		idempotency.Middleware("POST /affiliates/apply"),
		svc.Apply)
	router.Post("/affiliates/login",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /affiliates/login",
			Limit:   5,
			Window:  15 * time.Minute,
			KeyFunc: ratelimit.ByIPAndBodyField("email"),
		}),
		svc.Login)

	// The /me group is registered before the /affiliates admin group so its
	// routes are not shadowed by the group's TenantMW (Fiber matches the first
	// registered route for overlapping prefix families; see the reviews/
	// bundles comment in cmd/api/main.go).
	me := router.Group("/affiliates/me", auth.AffiliateAuthMW(pool, secret))
	me.Get("/dashboard", svc.Dashboard)
	me.Get("/commissions", svc.Commissions)
	me.Post("/payout-request", svc.PayoutRequest)

	admin := router.Group("/affiliates", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListAffiliates)
	admin.Patch("/:id/status", svc.UpdateStatus)

	router.Patch("/affiliate-payouts/:id", auth.TenantMW(pool, secret), svc.UpdatePayoutStatus)
}
package cart

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// RegisterRoutes mounts the Phase 4 guest-cart surface on the /api/v1 router.
// Every route resolves the tenant from X-Tenant-ID and the guest session from
// the shopkeet_session cookie / X-Customer-Session header via auth.CustomerMW.
// POST /cart/discount is idempotency-guarded and rate-limited (Phase 14) so a
// retried apply doesn't double-apply a promo and a discount-code brute force
// is throttled per cart session.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, svc *Service, limiter *ratelimit.Limiter) {
	g := router.Group("/cart", auth.CustomerMW(pool))
	g.Get("/", svc.GetCart)
	g.Post("/", svc.AddItem)
	g.Post("/discount",
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /cart/discount",
			Limit:   20,
			Window:  time.Hour,
			KeyFunc: ratelimit.BySession(),
		}),
		idempotency.Middleware("POST /cart/discount"),
		svc.ApplyDiscount)
	g.Patch("/items/:id", svc.UpdateItemQuantity)
	g.Delete("/items/:id", svc.RemoveItem)
}

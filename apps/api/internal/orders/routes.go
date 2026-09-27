package orders

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// RegisterRoutes mounts the Phase 5 checkout/orders surface on /api/v1:
//
//	POST /checkout              (Customer — guest session, or customer JWT to link the order, Phase 11)
//	POST /orders/draft          (Admin — merchant-entered phone/WhatsApp order, Phase 15)
//	GET  /orders/:id            (Customer — verified by phone[/email])
//	POST /orders/:id/returns    (Customer — verified by phone[/email], or Admin, Phase 15)
//	GET  /orders                (Admin)
//	PATCH /orders/:id/status    (Admin)
//	PATCH /orders/:id/note      (Admin — internal note, never shown to customer)
//	GET  /returns               (Admin)
//	PATCH /returns/:id/status   (Admin — received restocks the returned variants)
//
// POST /checkout and POST /orders/draft are idempotency-guarded (Phase 14): a
// client replaying the same Idempotency-Key gets the stored order instead of a
// second one. POST /checkout is additionally rate-limited at 30/hr per IP.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service, limiter *ratelimit.Limiter) {
	router.Post("/checkout",
		auth.CustomerOrGuestMW(pool, secret),
		limiter.Middleware(ratelimit.Entry{
			Route:   "POST /checkout",
			Limit:   30,
			Window:  time.Hour,
			KeyFunc: ratelimit.ByIP(),
		}),
		idempotency.Middleware("POST /checkout"),
		svc.Checkout)
	router.Post("/orders/draft",
		auth.TenantMW(pool, secret),
		idempotency.Middleware("POST /orders/draft"),
		svc.CreateDraftOrder)
	router.Post("/orders/:id/returns",
		auth.MerchantOrCustomerMW(pool, secret),
		svc.CreateReturn)
	router.Get("/orders/:id", auth.CustomerMW(pool), svc.GetOrder)

	admin := router.Group("/orders", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListOrders)
	admin.Patch("/:id/status", svc.UpdateStatus)
	admin.Patch("/:id/note", svc.UpdateNote)

	returns := router.Group("/returns", auth.TenantMW(pool, secret))
	returns.Get("/", svc.ListReturns)
	returns.Patch("/:id/status", svc.UpdateReturnStatus)
}

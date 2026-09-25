package orders

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 5 checkout/orders surface on /api/v1:
//
//	POST /checkout              (Customer — guest session, or customer JWT to link the order, Phase 11)
//	GET  /orders/:id            (Customer — verified by phone[/email])
//	GET  /orders                (Admin)
//	PATCH /orders/:id/status    (Admin)
//	PATCH /orders/:id/note      (Admin — internal note, never shown to customer)
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Post("/checkout", auth.CustomerOrGuestMW(pool, secret), svc.Checkout)
	router.Get("/orders/:id", auth.CustomerMW(pool), svc.GetOrder)

	admin := router.Group("/orders", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListOrders)
	admin.Patch("/:id/status", svc.UpdateStatus)
	admin.Patch("/:id/note", svc.UpdateNote)
}

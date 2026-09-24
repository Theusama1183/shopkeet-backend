package orders

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 5 checkout/orders surface on /api/v1:
//
//	POST /checkout              (Customer — guest session, COD)
//	GET  /orders/:id            (Customer — verified by phone[/email])
//	GET  /orders                (Admin)
//	PATCH /orders/:id/status    (Admin)
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Post("/checkout", auth.CustomerMW(pool), svc.Checkout)
	router.Get("/orders/:id", auth.CustomerMW(pool), svc.GetOrder)

	admin := router.Group("/orders", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListOrders)
	admin.Patch("/:id/status", svc.UpdateStatus)
}

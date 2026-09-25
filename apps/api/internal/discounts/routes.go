package discounts

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 10 discounts surface on /api/v1:
//
//	GET    /discounts          (Admin)
//	POST   /discounts          (Admin)
//	GET    /discounts/:id      (Admin)
//	PATCH  /discounts/:id      (Admin)
//	DELETE /discounts/:id      (Admin)
//
// The customer-facing POST /cart/discount lives in the cart package (it needs
// the CustomerMW group); checkout validation lives in internal/orders.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	g := router.Group("/discounts", auth.TenantMW(pool, secret))
	g.Get("/", svc.ListDiscounts)
	g.Post("/", svc.CreateDiscount)
	g.Get("/:id", svc.GetDiscount)
	g.Patch("/:id", svc.UpdateDiscount)
	g.Delete("/:id", svc.DeleteDiscount)
}

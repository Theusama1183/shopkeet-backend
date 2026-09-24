package cart

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 4 guest-cart surface on the /api/v1 router.
// Every route resolves the tenant from X-Tenant-ID and the guest session from
// the shopkeet_session cookie / X-Customer-Session header via auth.CustomerMW.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, svc *Service) {
	g := router.Group("/cart", auth.CustomerMW(pool))
	g.Get("/", svc.GetCart)
	g.Post("/", svc.AddItem)
	g.Patch("/items/:id", svc.UpdateItemQuantity)
	g.Delete("/items/:id", svc.RemoveItem)
}

package shipping

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 9 shipping surface on /api/v1:
//
//	GET    /shipping/rates         (Public — ?country=&state=)
//	POST   /shipping/zones         (Admin)
//	PATCH  /shipping/zones/:id     (Admin)
//	DELETE /shipping/zones/:id     (Admin)
//	POST   /shipping/rates         (Admin)
//	PATCH  /shipping/rates/:id     (Admin)
//	DELETE /shipping/rates/:id     (Admin)
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Get("/shipping/rates", auth.PublicTenantMW(pool), svc.ListRates)

	admin := router.Group("/shipping", auth.TenantMW(pool, secret))
	admin.Post("/zones", svc.CreateZone)
	admin.Patch("/zones/:id", svc.UpdateZone)
	admin.Delete("/zones/:id", svc.DeleteZone)
	admin.Post("/rates", svc.CreateRate)
	admin.Patch("/rates/:id", svc.UpdateRate)
	admin.Delete("/rates/:id", svc.DeleteRate)
}

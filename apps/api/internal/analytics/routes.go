package analytics

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 24 analytics surface on /api/v1:
//
//	GET /analytics/sales         (Admin)
//	GET /analytics/top-products  (Admin)
//	GET /analytics/conversion    (Admin)
//
// All three are merchant-facing dashboard reads behind TenantMW (JWT + SET
// LOCAL app.current_tenant); RLS scopes every aggregate to the caller's tenant
// and the migration-0026 indexes cover the filters.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	g := router.Group("/analytics", auth.TenantMW(pool, secret))
	g.Get("/sales", svc.GetSales)
	g.Get("/top-products", svc.GetTopProducts)
	g.Get("/conversion", svc.GetConversion)
}

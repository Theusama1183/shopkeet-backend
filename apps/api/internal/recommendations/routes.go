package recommendations

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
)

// RegisterRoutes mounts the Phase 26 recommendations surface on /api/v1:
//
//	GET    /products/:id/recommendations             (Public — active picks only)
//	GET    /products/:id/recommendations             (Admin — all statuses)
//	POST   /products/:id/recommendations             (Admin — manual curation, idempotency-guarded)
//	DELETE /products/:id/recommendations/:rid        (Admin)
//
// Registered ahead of catalog's /products group so the sibling
// /products/:id/path segment never falls through to the products group's
// middleware set (same convention as reviews and bundles).
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Get("/products/:id/recommendations",
		auth.PublicTenantMW(pool), svc.ListRecommendations)

	admin := router.Group("/products/:id/recommendations", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListRecommendations)
	admin.Post("/", idempotency.Middleware("POST /products/:id/recommendations"), svc.CreateRecommendation)
	admin.Delete("/:rid", svc.DeleteRecommendation)
}

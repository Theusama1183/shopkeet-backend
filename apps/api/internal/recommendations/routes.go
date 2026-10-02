package recommendations

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
)

// RegisterRoutes mounts the Phase 26 recommendations surface on /api/v1:
//
//	GET    /products/:id/recommendations             (Public/Admin — public sees active picks only; admin sees all + status)
//	POST   /products/:id/recommendations             (Admin — manual curation, idempotency-guarded)
//	DELETE /products/:id/recommendations/:rid        (Admin)
//
// The list is a single GET under PublicOrAdminMW (the same dual-view pattern
// catalog's GET /products/:id uses): the Bearer JWT marks the request admin so
// the handler surfaces archived picks and the status field, while header-only
// storefront traffic gets active picks without leaking status. Registered ahead
// of catalog's /products group so the sibling /products/:id/path segment never
// falls through to the products group's middleware set.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Get("/products/:id/recommendations",
		auth.PublicOrAdminMW(pool, secret), svc.ListRecommendations)

	admin := router.Group("/products/:id/recommendations", auth.TenantMW(pool, secret))
	admin.Post("/", idempotency.Middleware("POST /products/:id/recommendations"), svc.CreateRecommendation)
	admin.Delete("/:rid", svc.DeleteRecommendation)
}

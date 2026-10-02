package bundles

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
)

// RegisterRoutes mounts the Phase 25 bundles + quantity-break surface on
// /api/v1:
//
//	GET    /bundles                    (Public — active only)
//	GET    /bundles                    (Admin — all, ?status= filter)
//	POST   /bundles                    (Admin — idempotency-guarded)
//	GET    /bundles/:id                (Admin)
//	PATCH  /bundles/:id                (Admin — items replaced when provided)
//	DELETE /bundles/:id                (Admin)
//	GET    /products/:id/quantity-breaks            (Admin)
//	POST   /products/:id/quantity-breaks            (Admin — idempotency-guarded)
//	PATCH  /products/:id/quantity-breaks/:breakID   (Admin)
//	DELETE /products/:id/quantity-breaks/:breakID   (Admin)
//
// The customer-facing POST /cart/bundle lives in the cart package (it needs
// the CustomerMW group); checkout pricing lives in internal/orders. Registered
// ahead of catalog's /products group so Fiber never shadows the sibling
// quantity-break paths with the products group's middleware set.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Get("/bundles", auth.PublicTenantMW(pool), svc.ListPublicBundles)

	admin := router.Group("/bundles", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListBundles)
	admin.Post("/", idempotency.Middleware("POST /bundles"), svc.CreateBundle)
	admin.Get("/:id", svc.GetBundle)
	admin.Patch("/:id", svc.UpdateBundle)
	admin.Delete("/:id", svc.DeleteBundle)

	breaks := router.Group("/products/:id/quantity-breaks", auth.TenantMW(pool, secret))
	breaks.Get("/", svc.ListQuantityBreaks)
	breaks.Post("/", idempotency.Middleware("POST /products/:id/quantity-breaks"), svc.CreateQuantityBreak)
	breaks.Patch("/:breakID", svc.UpdateQuantityBreak)
	breaks.Delete("/:breakID", svc.DeleteQuantityBreak)
}

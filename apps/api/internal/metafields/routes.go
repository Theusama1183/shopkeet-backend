// routes.go registers the Phase 28 metafield endpoints. These are explicit
// routes on the router (like the bundle/recommendation/review siblings), NOT
// inside a /products group: Fiber v2 applies the first-registered middleware
// family to an overlapping prefix, so /products/:id/metafields must be
// registered before catalog mounts /products/:id (which would otherwise shadow
// this branch with the PublicOrAdminMW and drop the admin write paths).
package metafields

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the metafield surface; call from main.go BEFORE
// catalog.RegisterRoutes.
func RegisterRoutes(r fiber.Router, sc *Service, pool *pgxpool.Pool, secret string) {
	admin := r.Group("/products/:id/metafields", auth.TenantMW(pool, secret))
	admin.Get("/", sc.List)
	admin.Put("/:key", sc.Upsert)
	admin.Delete("/:key", sc.Delete)
}
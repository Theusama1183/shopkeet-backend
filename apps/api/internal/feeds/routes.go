// routes.go registers the Phase 30 product-feeds endpoints. They are public
// storefront routes mounted with PublicTenantMW (tenant resolved from the
// X-Tenant-ID header), like every public surface.
package feeds

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the feed endpoints on the top-level router.
func RegisterRoutes(r fiber.Router, sc *Service, pool *pgxpool.Pool) {
	r.Get("/feeds/google-shopping.xml", auth.PublicTenantMW(pool), sc.GoogleShoppingXML)
	r.Get("/feeds/meta-catalog.csv", auth.PublicTenantMW(pool), sc.MetaCatalogCSV)
}
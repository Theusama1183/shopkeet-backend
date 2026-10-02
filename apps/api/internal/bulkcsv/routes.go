// routes.go registers the Phase 29 bulk import/export endpoints. Explicit
// routes on the router, registered BEFORE catalog mounts /products/:id —
// otherwise GET /products/export and GET /products/import would be captured by
// the /products/:id public handler with "export"/"import" as the id.
package bulkcsv

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the bulk CSV surface; call from main.go BEFORE
// catalog.RegisterRoutes.
func RegisterRoutes(r fiber.Router, sc *Service, pool *pgxpool.Pool, secret string) {
	r.Post("/products/import", auth.TenantMW(pool, secret), sc.Import)
	r.Get("/products/import/:jobId", auth.TenantMW(pool, secret), sc.JobStatus)
	r.Get("/products/export", auth.TenantMW(pool, secret), sc.Export)
}
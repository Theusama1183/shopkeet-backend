// routes.go — Phase 33 admin surface. Customer tags & segments live under
// /customers behind TenantMW:
//
//	GET    /customers             (Admin — list all, or ?tag=vip for a segment)
//	POST   /customers/:id/tags    (Admin — {"tag":"vip"}; idempotent)
//	DELETE /customers/:id/tags?tag=vip (Admin)
//
// The public /customers/signup|login|me routes (Phase 11) keep their own
// middleware set; nothing here extends that public group.
package customertags

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

func RegisterRoutes(router fiber.Router, svc *Service, pool *pgxpool.Pool, secret string) {
	admin := router.Group("/customers", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListCustomers)
	admin.Post("/:id/tags", svc.AddTag)
	admin.Delete("/:id/tags", svc.RemoveTag)
}
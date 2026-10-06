package search

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the admin record search:
//
//	GET /search  (Admin)
//
// Merchant-only behind TenantMW (JWT + SET LOCAL app.current_tenant). There is
// no public sibling on this prefix, so the group middleware cannot leak to one
// (known-gotchas: Fiber applies group middleware per prefix — this group OWNS
// /search entirely). RLS scopes to the caller's tenant; the handler also
// predicates tenant_id explicitly.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	g := router.Group("/search", auth.TenantMW(pool, secret))
	g.Get("/", svc.Search)
}
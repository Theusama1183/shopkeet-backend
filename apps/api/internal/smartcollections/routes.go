// routes.go — Phase 31 admin surface. The smart/rule collection concept rides
// on top of categories: POST/PATCH/DELETE give merchants full CRUD, and the
// public GET /categories (catalog) now exposes is_smart + rules so a
// pattern-matching storefront can treat them however it wants.
//
// Routes are registered with the tenant middleware inline instead of a
// router.Group: Fiber v2 merges the group middleware onto any later-registered
// route whose path matches the group's exact static prefix, which would put
// catalog's public GET /categories behind TenantMW (verified by probe).
package smartcollections

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the merchant category CRUD under /categories with the
// tenant middleware (borrowed from the catalog group).
func RegisterRoutes(r fiber.Router, s *Service, pool *pgxpool.Pool, secret string) {
	authMW := auth.TenantMW(pool, secret)
	r.Post("/categories", authMW, s.CreateCategory)
	r.Patch("/categories/:id", authMW, s.UpdateCategory)
	r.Delete("/categories/:id", authMW, s.DeleteCategory)
}

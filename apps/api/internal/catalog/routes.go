package catalog

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 3 catalog surface onto the /api/v1 router.
// Storefront endpoints resolve the tenant from X-Tenant-ID (set by Next.js
// middleware per docs/03-architecture.md) and are public; admin endpoints sit
// behind TenantMW (JWT). GET /products/:id is PublicOrAdminMW: shoppers see
// active products only, the merchant sees any status.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Get("/products", auth.PublicTenantMW(pool), svc.ListProducts)
	router.Get("/products/:id", auth.PublicOrAdminMW(pool, secret), svc.GetProduct)

	admin := router.Group("/products", auth.TenantMW(pool, secret))
	admin.Post("/", svc.CreateProduct)
	admin.Patch("/:id", svc.UpdateProduct)
	admin.Delete("/:id", svc.DeleteProduct)
	admin.Post("/:id/images", svc.AddImage)
	admin.Delete("/:id/images/:imageId", svc.RemoveImage)

	router.Get("/categories", auth.PublicTenantMW(pool), svc.ListCategories)
}

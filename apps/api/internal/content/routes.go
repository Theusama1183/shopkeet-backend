package content

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 6 content surface onto the /api/v1 router.
// Public storefront endpoints resolve the tenant from X-Tenant-ID (auth.
// PublicTenantMW) and serve only published rows; admin endpoints sit behind
// TenantMW (JWT). The Puck editor's onPublish saves layout JSON verbatim
// through the admin endpoints below.
//
//	GET /posts            (public: one by post_type+route, or a post_type list)
//	POST/PATCH/DELETE /posts[/:id]   (admin; PATCH route change auto-redirects)
//	GET /templates/:template_type    (public, scope=default published)
//	PUT /templates/:template_type    (admin upsert)
//	GET /sections                    (public, published, by section_type)
//	POST/PATCH/DELETE /sections[/:id] (admin)
//	GET /redirects                   (admin)
//	POST/DELETE /redirects[/:id]     (admin)
//	GET /redirects/lookup?path=      (public, used by Next.js middleware)
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Get("/posts", auth.PublicTenantMW(pool), svc.GetPosts)
	router.Get("/templates/:template_type", auth.PublicTenantMW(pool), svc.GetTemplate)
	router.Get("/sections", auth.PublicTenantMW(pool), svc.GetSections)
	router.Get("/redirects/lookup", auth.PublicTenantMW(pool), svc.LookupRedirect)

	adminPosts := router.Group("/posts", auth.TenantMW(pool, secret))
	adminPosts.Post("/", svc.CreatePost)
	adminPosts.Patch("/:id", svc.UpdatePost)
	adminPosts.Delete("/:id", svc.DeletePost)

	adminTemplates := router.Group("/templates", auth.TenantMW(pool, secret))
	adminTemplates.Put("/:template_type", svc.PutTemplate)

	adminSections := router.Group("/sections", auth.TenantMW(pool, secret))
	adminSections.Post("/", svc.CreateSection)
	adminSections.Patch("/:id", svc.UpdateSection)
	adminSections.Delete("/:id", svc.DeleteSection)

	adminRedirects := router.Group("/redirects", auth.TenantMW(pool, secret))
	adminRedirects.Get("/", svc.ListRedirects)
	adminRedirects.Post("/", svc.CreateRedirect)
	adminRedirects.Delete("/:id", svc.DeleteRedirect)
}

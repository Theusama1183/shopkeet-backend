package media

import (
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 2 media surface behind tenant auth:
//
//	POST   /media/upload-url   (admin) — presigned R2 PUT URL + r2_key
//	POST   /media              (admin) — record an uploaded asset
//	GET    /media              (admin) — list this tenant's assets
//	DELETE /media/:id          (admin) — delete object + row
//
// Token validation and the RLS-scoped request transaction come from
// auth.TenantMW; handlers read pgx.Tx from c.Locals("tx").
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	g := router.Group("/media", auth.TenantMW(pool, secret))
	g.Post("/upload-url", svc.UploadURL)
	g.Post("/", svc.Confirm)
	g.Get("/", svc.List)
	g.Delete("/:id", svc.Delete)
}

// PresignTTL is the default lifetime of issued upload URLs.
const PresignTTL = 15 * time.Minute

package reviews

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 16 review surface on /api/v1:
//
//	POST   /products/:id/reviews   (Customer — customer JWT; verified if delivered order exists)
//	GET    /products/:id/reviews   (Public — published only + live rating aggregates)
//	GET    /reviews                (Admin — all statuses for approve/reject)
//	PATCH  /reviews/:id            (Admin — publish/reject; recomputes product rating)
//	DELETE /reviews/:id            (Admin — recomputes product rating)
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	router.Post("/products/:id/reviews",
		auth.CustomerAuthMW(pool, secret), svc.CreateReview)
	router.Get("/products/:id/reviews",
		auth.PublicTenantMW(pool), svc.ListReviews)

	admin := router.Group("/reviews", auth.TenantMW(pool, secret))
	admin.Get("/", svc.ListAllReviews)
	admin.Patch("/:id", svc.UpdateReviewStatus)
	admin.Delete("/:id", svc.DeleteReview)
}

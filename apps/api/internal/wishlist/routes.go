package wishlist

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
)

// RegisterRoutes mounts the Phase 22 wishlist surface on /api/v1 (all
// Customer-scoped):
//
//	GET    /customers/me/wishlist          (Customer — every saved product)
//	POST   /customers/me/wishlist          (Customer — body {product_id})
//	POST   /customers/me/wishlist/:productId (Customer — route-param add)
//	DELETE /customers/me/wishlist/:productId (Customer — remove)
//
// The group prefix is /customers/me, shared with Phase 11's profile/orders/
// addresses group; Fiber matches these longer prefixes first, so no conflict.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	me := router.Group("/customers/me/wishlist", auth.CustomerAuthMW(pool, secret))
	me.Get("/", svc.ListWishlist)
	me.Post("/", svc.AddWishlistItem)
	me.Post("/:productId", svc.AddWishlistItem)
	me.Delete("/:productId", svc.RemoveWishlistItem)
}

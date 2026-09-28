package giftcards

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
)

// RegisterRoutes mounts the Phase 18 gift-cards surface on /api/v1:
//
//	GET  /gift-cards  (Admin)
//	POST /gift-cards  (Admin — issue; empty code auto-generates)
//
// The customer-facing POST /cart/gift-card lives in the cart package (it needs
// the CustomerMW group); checkout validation lives in internal/orders.
// POST /gift-cards is idempotency-guarded (Phase 14) so a retry doesn't issue
// a duplicate card.
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	g := router.Group("/gift-cards", auth.TenantMW(pool, secret))
	g.Get("/", svc.ListGiftCards)
	g.Post("/", idempotency.Middleware("POST /gift-cards"), svc.CreateGiftCard)
}
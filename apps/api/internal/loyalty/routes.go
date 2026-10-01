package loyalty

import (
	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
)

// RegisterRoutes mounts the loyalty API under the v1 router:
//
//	GET  /v1/customers/me/loyalty   customer's balance + ledger
//	GET  /v1/customers/me/referral  customer's unique referral code
//	POST /v1/loyalty/redeem         convert points into a cart discount code
//
// All endpoints are customer-scoped (resolved via the customer JWT by
// CustomerAuthMW). Redeem is additionally idempotency-guarded so a client retry
// cannot spend the same points twice (Phase 14).
func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string, svc *Service) {
	me := router.Group("/customers/me", auth.CustomerAuthMW(pool, secret))
	me.Get("/loyalty", svc.GetLoyalty)
	me.Get("/referral", svc.GetReferral)

	router.Post("/loyalty/redeem",
		auth.CustomerAuthMW(pool, secret),
		idempotency.Middleware("POST /loyalty/redeem"),
		svc.Redeem)
}

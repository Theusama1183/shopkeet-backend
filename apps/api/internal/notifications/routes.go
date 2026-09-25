package notifications

import (
	"context"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string) {
	// GET /notifications/log — Admin only
	router.Get("/notifications/log", auth.TenantMW(pool, secret), listLog)
}

func listLog(c *fiber.Ctx) error {
	tx, ok := c.Locals("tx").(interface {
		Query(context.Context, string, ...any) (interface {
			Next() bool
			Scan(...any) error
			Close()
		}, error)
	})
	if !ok {
		return httperr.ErrInternalServerError
	}

	rows, err := tx.Query(c.Context(), `
		SELECT id, notification_type, recipient, order_id, status, sent_at
		FROM notification_log
		ORDER BY sent_at DESC
		LIMIT 100`)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	var out []fiber.Map
	for rows.Next() {
		var id, typ, recipient, status, sentAt string
		var orderID *string
		if err := rows.Scan(&id, &typ, &recipient, &orderID, &status, &sentAt); err != nil {
			return httperr.ErrInternalServerError
		}
		m := fiber.Map{
			"id":                id,
			"notification_type": typ,
			"recipient":         recipient,
			"status":            status,
			"sent_at":           sentAt,
		}
		if orderID != nil {
			m["order_id"] = *orderID
		}
		out = append(out, m)
	}
	return c.JSON(fiber.Map{"notifications": out})
}

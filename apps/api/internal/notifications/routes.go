package notifications

import (
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

func RegisterRoutes(router fiber.Router, pool *pgxpool.Pool, secret string) {
	// GET /notifications/log — Admin only
	router.Get("/notifications/log", auth.TenantMW(pool, secret), listLog)
}

func listLog(c *fiber.Ctx) error {
	// Must be the pgx.Tx assertion: a structurally-identical anonymous interface
	// never matches, because pgx.Tx.Query returns the concrete pgx.Rows type and
	// Go method sets require identical signatures, not assignable ones.
	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}

	rows, err := tx.Query(c.Context(), `
		SELECT id, notification_type, recipient, order_id, status, sent_at
		FROM notification_log
		ORDER BY sent_at DESC
		LIMIT 100`)
	if err != nil {
		log.Printf("[notifications] log query failed: %v", err)
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	var out = []fiber.Map{}
	for rows.Next() {
		// sent_at is timestamptz: pgx cannot scan that into a string, so it is
		// decoded as time.Time and formatted the same way as the other endpoints.
		var id, typ, recipient, status string
		var sentAt time.Time
		var orderID *string
		if err := rows.Scan(&id, &typ, &recipient, &orderID, &status, &sentAt); err != nil {
			log.Printf("[notifications] log scan failed: %v", err)
			return httperr.ErrInternalServerError
		}
		if err := rows.Err(); err != nil {
			log.Printf("[notifications] log rows failed: %v", err)
			return httperr.ErrInternalServerError
		}
		m := fiber.Map{
			"id":                id,
			"notification_type": typ,
			"recipient":         recipient,
			"status":            status,
			"sent_at":           sentAt.Format(time.RFC3339),
		}
		if orderID != nil {
			m["order_id"] = *orderID
		}
		out = append(out, m)
	}
	return c.JSON(fiber.Map{"notifications": out})
}

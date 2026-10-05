package cart

import (
	"time"

	"github.com/gofiber/fiber/v2"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// ListAbandoned handles GET /carts/abandoned (Admin). Lists checkouts that
// started but never converted: carts with a captured customer_email that still
// have items (checkout deletes the cart, so a surviving row with lines is by
// construction one that produced no order). recovery_sent_at surfaces whether
// the Phase 17 sweep has already emailed that buyer. RLS scopes the aggregate
// to the caller's tenant via TenantMW.
func (s *Service) ListAbandoned(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tid, _ := c.Locals("tenant_id").(string)

	rows, err := tx.Query(c.Context(), `
		SELECT c.id, c.customer_email, c.created_at, c.last_activity_at, c.recovery_sent_at,
		       COUNT(ci.id)::int, COALESCE(SUM(ci.quantity * v.price_cents), 0)::int
		FROM carts c
		JOIN cart_items ci ON ci.cart_id = c.id AND ci.tenant_id = c.tenant_id
		JOIN product_variants v ON v.id = ci.variant_id
		WHERE c.tenant_id = $1
		  AND c.customer_email IS NOT NULL
		GROUP BY c.id
		ORDER BY c.created_at DESC`, tid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	carts := make([]fiber.Map, 0)
	for rows.Next() {
		var id, email string
		var createdAt, lastActivity *time.Time
		var recoverySentAt *time.Time
		var itemCount, totalCents int
		if err := rows.Scan(&id, &email, &createdAt, &lastActivity, &recoverySentAt,
			&itemCount, &totalCents); err != nil {
			return httperr.ErrInternalServerError
		}
		carts = append(carts, fiber.Map{
			"id":                id,
			"customer_email":    email,
			"created_at":        timestamptz(createdAt),
			"last_activity_at":  timestamptz(lastActivity),
			"recovery_sent_at":  timestamptz(recoverySentAt),
			"item_count":        itemCount,
			"total_cents":       totalCents,
		})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"carts": carts})
}

func timestamptz(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().Format(time.RFC3339)
}
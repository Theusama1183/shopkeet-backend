package catalog

// Phase 19 — back-in-stock email capture (docs/08-hardening-and-features-
// build-spec.md). A shopper subscribes to an out-of-stock, non-preorderable
// variant; when a merchant PATCH restocks it (inventory 0 -> positive) the
// notifications subscriber emails every subscription with notified_at IS NULL
// exactly once. This handler only records interest — never sends.

import (
	"context"
	"errors"
	"regexp"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shopkeet/api/internal/platform/httperr"
)

var notifyEmailRe = regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`)

type notifyMeRequest struct {
	Email string `json:"email"`
}

// NotifyMe handles POST /products/:id/variants/:variantId/notify-me (Public).
// Only out-of-stock, non-preorderable variants accept signups; preorder buyers
// don't need the email, and a stocked variant has nothing to alert for. A
// repeated subscription for the same email is a 409 (per-variant UNIQUE).
func (s *Service) NotifyMe(c *fiber.Ctx) error {
	var req notifyMeRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if !notifyEmailRe.MatchString(req.Email) {
		return httperr.C(fiber.StatusBadRequest, "valid email required")
	}
	if len(req.Email) > 254 {
		return httperr.C(fiber.StatusBadRequest, "valid email required")
	}

	tid := tenantID(c)
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	productID := c.Params("id")
	variantID := c.Params("variantId")

	var inventory int
	var allowPreorder bool
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT v.inventory_count, v.allow_preorder, (p.status = 'active' AND v.status = 'active')
		FROM product_variants v
		JOIN products p ON p.id = v.product_id
		WHERE v.id = $1 AND v.product_id = $2`, variantID, productID).
		Scan(&inventory, &allowPreorder, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return httperr.C(fiber.StatusNotFound, "variant not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if allowPreorder {
		return httperr.C(fiber.StatusBadRequest, "preorderable variants don't accept back-in-stock signups")
	}
	if inventory > 0 {
		return httperr.C(fiber.StatusBadRequest, "variant is in stock")
	}

	var subscriptionID string
	if err := savepoint(ctx, tx, func() error {
		var e error
		subscriptionID, e = insertNotifySubscription(ctx, tx, tid, variantID, req.Email)
		return e
	}); err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "already subscribed")
		}
		return httperr.ErrInternalServerError
	}

	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"id": subscriptionID, "variant_id": variantID, "email": req.Email})
}

// insertNotifySubscription records a back-in-stock signup. The UNIQUE
// (tenant_id, variant_id, lower(email)) constraint is the dedupe backstop.
func insertNotifySubscription(ctx context.Context, tx pgx.Tx, tid, variantID, email string) (string, error) {
	id := uuid.NewString()
	_, err := tx.Exec(ctx, `
		INSERT INTO back_in_stock_subscriptions (id, tenant_id, variant_id, email)
		VALUES ($1, $2, $3, $4)`, id, tid, variantID, email)
	return id, err
}
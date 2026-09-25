// Package discounts implements Phase 10 of the expansion spec: admin-managed
// discount codes (percentage or fixed_amount, with a minimum subtotal, validity
// window and optional usage cap), a POST /cart/discount apply endpoint, and the
// checkout integration in internal/orders that re-validates the code and claims
// one usage inside the order transaction. Codes are scoped by RLS exactly like
// every other tenant table; the discount is snapshotted onto the order so later
// edits or deletions never change an existing order's total.
package discounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service implements the admin discounts surface. Handlers execute inside the
// RLS-scoped request transaction opened by TenantMW, so every query is bound to
// the resolved tenant.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// --- shared row + validation ----------------------------------------------------

type discountRow struct {
	id           string
	code         string
	dType        string
	valuePercent *int
	valueCents   *int
	minSubtotal  *int
	startsAt     *time.Time
	endsAt       *time.Time
	usageLimit   *int
	timesUsed    int
	status       string
	createdAt    time.Time
}

const discountSelect = `
	SELECT id, code, type, value_percent, value_cents, min_subtotal_cents,
	       starts_at, ends_at, usage_limit, times_used, status, created_at
	FROM discounts`

// Quote is the resolved discount line for a cart or checkout: the snapshotted
// code and the computed discount_cents for the given subtotal.
type Quote struct {
	Code          string
	DiscountCents int
}

// CodeError is a client-facing validation failure (wrong code, inactive,
// expired, below minimum, usage cap hit). HTTP-facing callers translate it to a
// 4xx JSON error; anything else is a 500.
type CodeError struct {
	Status  int
	Message string
}

func (e *CodeError) Error() string { return e.Message }

func invalid(status int, msg string) error {
	return &CodeError{Status: status, Message: msg}
}

func (d discountRow) validate(subtotalCents int) error {
	if d.status != "active" {
		return invalid(fiber.StatusBadRequest, "discount code is not active")
	}
	now := time.Now().UTC()
	if d.startsAt != nil && now.Before(*d.startsAt) {
		return invalid(fiber.StatusBadRequest, "discount code is not active yet")
	}
	if d.endsAt != nil && !now.Before(*d.endsAt) {
		return invalid(fiber.StatusBadRequest, "discount code has expired")
	}
	if d.minSubtotal != nil && subtotalCents < *d.minSubtotal {
		return invalid(fiber.StatusBadRequest, "order subtotal is below the minimum for this discount")
	}
	if d.usageLimit != nil && d.timesUsed >= *d.usageLimit {
		return invalid(fiber.StatusBadRequest, "discount code usage limit reached")
	}
	return nil
}

// discount calculates the discount_cents for a subtotal: percentage off the
// subtotal, or a fixed amount capped at the subtotal (never below zero).
func (d discountRow) discount(subtotalCents int) int {
	switch d.dType {
	case "percentage":
		if d.valuePercent == nil {
			return 0
		}
		return subtotalCents * *d.valuePercent / 100
	case "fixed_amount":
		if d.valueCents == nil {
			return 0
		}
		if subtotalCents < *d.valueCents {
			return subtotalCents
		}
		return *d.valueCents
	}
	return 0
}

func scanDiscount(row pgx.Row) (discountRow, error) {
	var d discountRow
	err := row.Scan(&d.id, &d.code, &d.dType, &d.valuePercent, &d.valueCents,
		&d.minSubtotal, &d.startsAt, &d.endsAt, &d.usageLimit, &d.timesUsed, &d.status, &d.createdAt)
	return d, err
}

// Resolve validates a code against a subtotal without writing. Used by the
// cart apply/read path where checkout remains authoritative.
func Resolve(ctx context.Context, tx pgx.Tx, code string, subtotalCents int) (*Quote, error) {
	d, err := scanDiscount(tx.QueryRow(ctx, discountSelect+" WHERE code = $1", code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid(fiber.StatusNotFound, "discount code not found")
	}
	if err != nil {
		return nil, err
	}
	if err := d.validate(subtotalCents); err != nil {
		return nil, err
	}
	return &Quote{Code: d.code, DiscountCents: d.discount(subtotalCents)}, nil
}

// Claim re-validates a code and, if valid, atomically increments times_used
// inside the caller's (checkout) transaction. The FOR UPDATE lock serializes
// concurrent checkouts so a usage_limit=1 code is consumed exactly once.
func Claim(ctx context.Context, tx pgx.Tx, code string, subtotalCents int) (*Quote, error) {
	d, err := scanDiscount(tx.QueryRow(ctx, discountSelect+" WHERE code = $1 FOR UPDATE", code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid(fiber.StatusNotFound, "discount code not found")
	}
	if err != nil {
		return nil, err
	}
	if err := d.validate(subtotalCents); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		"UPDATE discounts SET times_used = times_used + 1 WHERE id = $1", d.id); err != nil {
		return nil, err
	}
	return &Quote{Code: d.code, DiscountCents: d.discount(subtotalCents)}, nil
}

// --- helpers --------------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

func toJSON(d discountRow) fiber.Map {
	return fiber.Map{
		"id": d.id, "code": d.code, "type": d.dType,
		"value_percent":      intOrNil(d.valuePercent),
		"value_cents":        intOrNil(d.valueCents),
		"min_subtotal_cents": intOrNil(d.minSubtotal),
		"starts_at":          timeOrNil(d.startsAt),
		"ends_at":            timeOrNil(d.endsAt),
		"usage_limit":        intOrNil(d.usageLimit),
		"times_used":         d.timesUsed,
		"status":             d.status,
		"created_at":         d.createdAt.Format(time.RFC3339),
	}
}

func intOrNil(v *int) any {
	if v == nil {
		return nil
	}
	return *v
}

func timeOrNil(v *time.Time) any {
	if v == nil {
		return nil
	}
	return v.Format(time.RFC3339)
}

// parseTimePtr parses an optional RFC3339 timestamp from a request field.
func parseTimePtr(s string) (*time.Time, error) {
	if s == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return nil, fmt.Errorf("invalid timestamp %q: %w", s, err)
	}
	return &t, nil
}

// validateFields checks that a (possibly partial) discount row is well-formed:
// a valid type, exactly one of value_percent/value_cents depending on the type,
// and sensible limits. Returns a client-facing 400 on violation.
func validateFields(code, dType string, valuePercent, valueCents, minSubtotal, usageLimit *int) error {
	if strings.TrimSpace(code) == "" {
		return invalid(fiber.StatusBadRequest, "code required")
	}
	if dType == "" {
		return invalid(fiber.StatusBadRequest, "type required (percentage | fixed_amount)")
	}
	switch dType {
	case "percentage":
		if valuePercent == nil || *valuePercent < 1 || *valuePercent > 100 {
			return invalid(fiber.StatusBadRequest, "value_percent required and 1-100 for percentage discounts")
		}
	case "fixed_amount":
		if valueCents == nil || *valueCents <= 0 {
			return invalid(fiber.StatusBadRequest, "value_cents required and > 0 for fixed_amount discounts")
		}
	default:
		return invalid(fiber.StatusBadRequest, "type must be percentage or fixed_amount")
	}
	if minSubtotal != nil && *minSubtotal < 0 {
		return invalid(fiber.StatusBadRequest, "min_subtotal_cents cannot be negative")
	}
	if usageLimit != nil && *usageLimit < 1 {
		return invalid(fiber.StatusBadRequest, "usage_limit must be >= 1")
	}
	return nil
}

// --- admin handlers --------------------------------------------------------------

// ListDiscounts handles GET /discounts (Admin). Newest first.
func (s *Service) ListDiscounts(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(), discountSelect+" ORDER BY created_at DESC, id")
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	var out []fiber.Map
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		out = append(out, toJSON(d))
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	if out == nil {
		out = []fiber.Map{}
	}
	return c.JSON(fiber.Map{"discounts": out})
}

type discountRequest struct {
	Code             string `json:"code"`
	Type             string `json:"type"`
	ValuePercent     *int   `json:"value_percent"`
	ValueCents       *int   `json:"value_cents"`
	MinSubtotalCents *int   `json:"min_subtotal_cents"`
	StartsAt         string `json:"starts_at"`
	EndsAt           string `json:"ends_at"`
	UsageLimit       *int   `json:"usage_limit"`
	Status           string `json:"status"`
}

// CreateDiscount handles POST /discounts (Admin).
func (s *Service) CreateDiscount(c *fiber.Ctx) error {
	var req discountRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if err := validateFields(req.Code, req.Type, req.ValuePercent, req.ValueCents,
		req.MinSubtotalCents, req.UsageLimit); err != nil {
		return translate(err)
	}
	startsAt, err := parseTimePtr(req.StartsAt)
	if err != nil {
		return httperr.C(fiber.StatusBadRequest, "starts_at must be RFC3339")
	}
	endsAt, err := parseTimePtr(req.EndsAt)
	if err != nil {
		return httperr.C(fiber.StatusBadRequest, "ends_at must be RFC3339")
	}
	status := req.Status
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "disabled" {
		return httperr.C(fiber.StatusBadRequest, "status must be active or disabled")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	d, err := scanDiscount(tx.QueryRow(c.Context(), `
		INSERT INTO discounts (tenant_id, code, type, value_percent, value_cents,
			min_subtotal_cents, starts_at, ends_at, usage_limit, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING id, code, type, value_percent, value_cents, min_subtotal_cents,
			starts_at, ends_at, usage_limit, times_used, status, created_at`,
		tenantID(c), strings.ToUpper(strings.TrimSpace(req.Code)), req.Type,
		req.ValuePercent, req.ValueCents, req.MinSubtotalCents, startsAt, endsAt,
		req.UsageLimit, status))
	if err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "discount code already exists")
		}
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(toJSON(d))
}

// GetDiscount handles GET /discounts/:id (Admin).
func (s *Service) GetDiscount(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	d, err := scanDiscount(tx.QueryRow(c.Context(),
		discountSelect+" WHERE id = $1", c.Params("id")))
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "discount not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(toJSON(d))
}

// UpdateDiscount handles PATCH /discounts/:id (Admin). Accepts any subset of
// fields; absent fields are preserved. type/value fields are validated against
// the merged row so switching type without its value is rejected.
func (s *Service) UpdateDiscount(c *fiber.Ctx) error {
	var req discountRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	cur, err := scanDiscount(tx.QueryRow(ctx, discountSelect+" WHERE id = $1 FOR SHARE", c.Params("id")))
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "discount not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	code, dType := cur.code, cur.dType
	valuePercent, valueCents := cur.valuePercent, cur.valueCents
	minSubtotal, usageLimit := cur.minSubtotal, cur.usageLimit
	status := cur.status
	start, end := cur.startsAt, cur.endsAt
	if v := strings.TrimSpace(req.Code); v != "" {
		code = strings.ToUpper(v)
	}
	if req.Type != "" {
		dType = req.Type
	}
	if req.ValuePercent != nil {
		valuePercent = req.ValuePercent
	}
	if req.ValueCents != nil {
		valueCents = req.ValueCents
	}
	if req.MinSubtotalCents != nil {
		minSubtotal = req.MinSubtotalCents
	}
	if req.UsageLimit != nil {
		usageLimit = req.UsageLimit
	}
	if req.Status != "" {
		status = req.Status
	}
	if req.StartsAt != "" {
		t, err := parseTimePtr(req.StartsAt)
		if err != nil {
			return httperr.C(fiber.StatusBadRequest, "starts_at must be RFC3339")
		}
		start = t
	}
	if req.EndsAt != "" {
		t, err := parseTimePtr(req.EndsAt)
		if err != nil {
			return httperr.C(fiber.StatusBadRequest, "ends_at must be RFC3339")
		}
		end = t
	}
	if err := validateFields(code, dType, valuePercent, valueCents, minSubtotal, usageLimit); err != nil {
		return translate(err)
	}
	if status != "active" && status != "disabled" {
		return httperr.C(fiber.StatusBadRequest, "status must be active or disabled")
	}

	d, err := scanDiscount(tx.QueryRow(ctx, `
		UPDATE discounts SET code = $1, type = $2, value_percent = $3, value_cents = $4,
			min_subtotal_cents = $5, starts_at = $6, ends_at = $7, usage_limit = $8, status = $9
		WHERE id = $10
		RETURNING id, code, type, value_percent, value_cents, min_subtotal_cents,
			starts_at, ends_at, usage_limit, times_used, status, created_at`,
		code, dType, valuePercent, valueCents, minSubtotal, start, end, usageLimit, status,
		c.Params("id")))
	if err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "discount code already exists")
		}
		return httperr.ErrInternalServerError
	}
	return c.JSON(toJSON(d))
}

// DeleteDiscount handles DELETE /discounts/:id (Admin). Existing orders keep
// their snapshot; carts still carrying the code fail validation at checkout.
func (s *Service) DeleteDiscount(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), "DELETE FROM discounts WHERE id = $1", c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "discount not found")
	}
	return c.SendStatus(fiber.StatusNoContent)
}

// --- error translation -----------------------------------------------------------

func translate(err error) error {
	var ce *CodeError
	if errors.As(err, &ce) {
		return httperr.C(ce.Status, ce.Message)
	}
	return httperr.ErrInternalServerError
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// Package discounts implements Phase 10 of the expansion spec (admin-managed
// discount codes: percentage or fixed_amount, with a minimum subtotal, validity
// window and optional usage cap) and Phase 21 (advanced & automatic discounts:
// requires_code=false store-wide promos, and applies_to='shipping' "free
// shipping over $X"). It backs a POST /cart/discount apply endpoint and the
// checkout integration in internal/orders that picks the single best discount —
// the entered code or the best automatic one, never stacked — and claims one
// usage inside the order transaction. Codes are scoped by RLS exactly like
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
	code         *string
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
	appliesTo    string
	buyQuantity  *int
	getQuantity  *int
	requiresCode bool
	eligibleTag  *string
}

const discountSelect = `
	SELECT id, code, type, value_percent, value_cents, min_subtotal_cents,
	       starts_at, ends_at, usage_limit, times_used, status, created_at,
	       applies_to, buy_quantity, get_quantity, requires_code, eligible_tag
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
		&d.minSubtotal, &d.startsAt, &d.endsAt, &d.usageLimit, &d.timesUsed, &d.status, &d.createdAt,
		&d.appliesTo, &d.buyQuantity, &d.getQuantity, &d.requiresCode, &d.eligibleTag)
	return d, err
}

// tagEligible enforces discounts.eligible_tag (Phase 33): a code gated on a tag
// is only good for a signed-in customer who carries it. A nil customerID (guest
// checkout, unsigned cart apply) can never prove the tag, so the discount is
// refused there too — the checkout stays authoritative, like the expiry/cap
// re-validation it sits beside. Automatic (requires_code=false) promotions that
// are tag-gated simply stop matching for untagged/anonymous shoppers.
func tagEligible(ctx context.Context, tx pgx.Tx, d discountRow, customerID *string) error {
	if d.eligibleTag == nil {
		return nil
	}
	if customerID == nil {
		return invalid(fiber.StatusBadRequest, "discount requires the "+*d.eligibleTag+" tag (sign in to use this code)")
	}
	var has bool
	if err := tx.QueryRow(ctx,
		"SELECT EXISTS (SELECT 1 FROM customer_tags WHERE customer_id = $1 AND tag = $2)",
		*customerID, *d.eligibleTag).Scan(&has); err != nil {
		return err
	}
	if !has {
		return invalid(fiber.StatusBadRequest, "discount code requires the "+*d.eligibleTag+" tag")
	}
	return nil
}

// Resolve validates a code against a subtotal without writing, scoped to
// tenantID. Used by the cart apply/read path where checkout remains
// authoritative.
//
// The explicit tenant_id predicate is load-bearing, not redundant with RLS:
// production's DATABASE_URL connects as a superuser role, which bypasses FORCE
// ROW LEVEL SECURITY, so a code-only predicate would resolve another tenant's
// discount.
//
// Automatic discounts (requires_code=false) are deliberately not resolvable by
// code — they are not-for-entry, so entering one behaves like an unknown code.
// This also keeps them out of the checkout code-claim path where they would
// otherwise be double-applied (once as a code, once by AutoPick).
func Resolve(ctx context.Context, tx pgx.Tx, tenantID, code string, subtotalCents int, customerID *string) (*Quote, error) {
	d, err := scanDiscount(tx.QueryRow(ctx, discountSelect+" WHERE tenant_id = $1 AND code = $2", tenantID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid(fiber.StatusNotFound, "discount code not found")
	}
	if err != nil {
		return nil, err
	}
	if !d.requiresCode {
		return nil, invalid(fiber.StatusNotFound, "discount code not found")
	}
	if err := d.validate(subtotalCents); err != nil {
		return nil, err
	}
	if err := tagEligible(ctx, tx, d, customerID); err != nil {
		return nil, err
	}
	return &Quote{Code: *d.code, DiscountCents: d.discount(subtotalCents)}, nil
}

// Claim re-validates a code and, if valid, atomically increments times_used
// inside the caller's (checkout) transaction, scoped to tenantID. The FOR UPDATE
// lock serializes concurrent checkouts so a usage_limit=1 code is consumed
// exactly once. Automatic discounts are not claimable as codes (see Resolve).
func Claim(ctx context.Context, tx pgx.Tx, tenantID, code string, subtotalCents int, customerID *string) (*Quote, error) {
	d, err := scanDiscount(tx.QueryRow(ctx, discountSelect+" WHERE tenant_id = $1 AND code = $2 FOR UPDATE", tenantID, code))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid(fiber.StatusNotFound, "discount code not found")
	}
	if err != nil {
		return nil, err
	}
	if !d.requiresCode {
		return nil, invalid(fiber.StatusNotFound, "discount code not found")
	}
	if err := d.validate(subtotalCents); err != nil {
		return nil, err
	}
	if err := tagEligible(ctx, tx, d, customerID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		"UPDATE discounts SET times_used = times_used + 1 WHERE id = $1 AND tenant_id = $2", d.id, tenantID); err != nil {
		return nil, err
	}
	return &Quote{Code: *d.code, DiscountCents: d.discount(subtotalCents)}, nil
}

// AutoQuote is an applied automatic discount: no code, one scope, and the
// computed discount_cents for the checkout subtotal/shipping. The scope is what
// the cents apply to — an 'order' discount cuts the goods subtotal (so it
// reduces the taxable base), a 'shipping' discount cuts the shipping cost (so
// tax is unaffected).
type AutoQuote struct {
	ID            string
	AppliesTo     string
	DiscountCents int
}

// AutoPick selects the single best automatic discount for the tenant inside the
// caller's (checkout) transaction. Eligible rows — requires_code=false, active,
// inside their validity window, under their usage cap — are locked FOR UPDATE in
// deterministic id order, so concurrent checkouts serialize and a claimed cap is
// never overshot. Each candidate's value is computed against the subtotal
// ('order') or the shipping cost ('shipping') and the best is measured in the
// same unit (cents of real money), so the winner is the best deal for the
// shopper regardless of scope. Values of zero or less are skipped.
//
// AutoPick has no write side effects besides the row locks: the caller decides
// (against any entered code) and claims the winner via ClaimAuto, so a
// discount's times_used is only burned when it is actually applied.
//
// Returns nil when nothing qualifies or every candidate is worth <= 0.
func AutoPick(ctx context.Context, tx pgx.Tx, tenantID string, subtotalCents, shippingCents int, customerID *string) (*AutoQuote, error) {
	rows, err := tx.Query(ctx, discountSelect+`
		WHERE tenant_id = $1 AND status = 'active' AND requires_code = false
		  AND (starts_at IS NULL OR starts_at <= now())
		  AND (ends_at IS NULL OR ends_at > now())
		  AND (usage_limit IS NULL OR times_used < usage_limit)
		ORDER BY id
		FOR UPDATE`, tenantID)
	if err != nil {
		return nil, err
	}
	// Drain the candidate rows and release the connection BEFORE evaluating
	// eligibility. Per-candidate checks like tagEligible issue their own
	// queries against the same transaction, and pgx forbids issuing a statement
	// while a previous result set is still open (would surface as "conn busy").
	// The FOR UPDATE locks are held until the transaction ends regardless.
	var cands []discountRow
	for rows.Next() {
		d, err := scanDiscount(rows)
		if err != nil {
			rows.Close()
				return nil, err
		}
		cands = append(cands, d)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	var best *AutoQuote
	for _, d := range cands {
		if err := d.validate(subtotalCents); err != nil {
			continue // e.g. subtotal below min_subtotal_cents — not eligible now
		}
		if err := tagEligible(ctx, tx, d, customerID); err != nil {
			continue // tag-gated promo — shopper/anonymous doesn't carry the tag
		}
		var cents int
		switch d.appliesTo {
		case "order":
			cents = d.discount(subtotalCents)
		case "shipping":
			cents = d.discount(shippingCents)
		default: // 'product' (BOGO) has no checkout logic yet
			continue
		}
		if cents <= 0 {
			continue
		}
		// Strict > keeps the first (lowest id) on ties — deterministic, so the
		// outcome never depends on scan order, time, or index choice.
		if best == nil || cents > best.DiscountCents {
			best = &AutoQuote{ID: d.id, AppliesTo: d.appliesTo, DiscountCents: cents}
		}
	}
	return best, nil
}

// ClaimAuto claims a discount already selected by AutoPick, re-validating and
// incrementing times_used in the caller's transaction. The row was locked by
// AutoPick's FOR UPDATE, so the validation/usage-check cannot observe a stale
// count; re-validating anyway keeps a single source of truth. Returns the
// computed quote for the discount's scope.
func ClaimAuto(ctx context.Context, tx pgx.Tx, tenantID, id string, subtotalCents, shippingCents int, customerID *string) (*AutoQuote, error) {
	d, err := scanDiscount(tx.QueryRow(ctx, discountSelect+" WHERE id = $1 AND tenant_id = $2 FOR UPDATE", id, tenantID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, invalid(fiber.StatusNotFound, "discount not found")
	}
	if err != nil {
		return nil, err
	}
	if err := d.validate(subtotalCents); err != nil {
		return nil, err
	}
	if err := tagEligible(ctx, tx, d, customerID); err != nil {
		return nil, err
	}
	var cents int
	switch d.appliesTo {
	case "order":
		cents = d.discount(subtotalCents)
	case "shipping":
		cents = d.discount(shippingCents)
	default:
		return nil, invalid(fiber.StatusBadRequest, "unsupported discount scope")
	}
	if _, err := tx.Exec(ctx,
		"UPDATE discounts SET times_used = times_used + 1 WHERE id = $1 AND tenant_id = $2", d.id, tenantID); err != nil {
		return nil, err
	}
	return &AutoQuote{ID: d.id, AppliesTo: d.appliesTo, DiscountCents: cents}, nil
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
		"id": d.id, "code": strp(d.code), "type": d.dType,
		"value_percent":      intOrNil(d.valuePercent),
		"value_cents":        intOrNil(d.valueCents),
		"min_subtotal_cents": intOrNil(d.minSubtotal),
		"starts_at":          timeOrNil(d.startsAt),
		"ends_at":            timeOrNil(d.endsAt),
		"usage_limit":        intOrNil(d.usageLimit),
		"times_used":         d.timesUsed,
		"status":             d.status,
		"applies_to":         d.appliesTo,
		"requires_code":      d.requiresCode,
		"buy_quantity":       intOrNil(d.buyQuantity),
		"get_quantity":       intOrNil(d.getQuantity),
		"eligible_tag":       strp(d.eligibleTag),
		"created_at":         d.createdAt.Format(time.RFC3339),
	}
}

func strp(v *string) any {
	if v == nil {
		return nil
	}
	return *v
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
// sensible limits, a valid applies scope, and code requirements. A code is only
// mandatory when the discount is code-entered; automatic discounts
// (requires_code=false) have no code. Returns a client-facing 400 on violation.
func validateFields(code, dType string, valuePercent, valueCents, minSubtotal, usageLimit *int,
	requiresCode bool, appliesTo string, buyQuantity, getQuantity *int) error {
	switch appliesTo {
	case "":
		appliesTo = "order"
	case "order", "shipping":
	case "product":
		return invalid(fiber.StatusBadRequest, "product (BOGO) discounts are not supported yet")
	default:
		return invalid(fiber.StatusBadRequest, "applies_to must be order, shipping or product")
	}
	if buyQuantity != nil || getQuantity != nil {
		return invalid(fiber.StatusBadRequest, "buy_quantity/get_quantity (BOGO) are not supported yet")
	}
	if requiresCode && strings.TrimSpace(code) == "" {
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

// ListDiscounts handles GET /discounts (Admin). Newest first. Scoped to the
// caller's tenant explicitly.
func (s *Service) ListDiscounts(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(),
		discountSelect+" WHERE tenant_id = $1 ORDER BY created_at DESC, id", tenantID(c))
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
	AppliesTo        string `json:"applies_to"`
	BuyQuantity      *int   `json:"buy_quantity"`
	GetQuantity      *int   `json:"get_quantity"`
	RequiresCode     *bool  `json:"requires_code"`
	EligibleTag      *string `json:"eligible_tag"`
}

// normalizeEligibleTag canonicalizes an optional tag gate: trimmed, lowercased;
// an empty value clears the gate (currently only meaningful on PATCH, where a
// nil EligibleTag preserves the existing gate and "" removes it).
func normalizeEligibleTag(v *string) *string {
	if v == nil {
		return nil
	}
	tag := strings.ToLower(strings.TrimSpace(*v))
	if tag == "" {
		return nil
	}
	return &tag
}

// CreateDiscount handles POST /discounts (Admin).
func (s *Service) CreateDiscount(c *fiber.Ctx) error {
	var req discountRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	appliesTo, requiresCode := req.AppliesTo, true
	if strings.TrimSpace(appliesTo) == "" {
		appliesTo = "order"
	}
	if req.RequiresCode != nil {
		requiresCode = *req.RequiresCode
	}
	if err := validateFields(req.Code, req.Type, req.ValuePercent, req.ValueCents,
		req.MinSubtotalCents, req.UsageLimit, requiresCode, appliesTo,
		req.BuyQuantity, req.GetQuantity); err != nil {
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
	var code *string
	if codeVal := strings.TrimSpace(req.Code); codeVal != "" {
		code = ptr(strings.ToUpper(codeVal))
	}
	eligibleTag := normalizeEligibleTag(req.EligibleTag)

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	d, err := scanDiscount(tx.QueryRow(c.Context(), `
		INSERT INTO discounts (tenant_id, code, type, value_percent, value_cents,
			min_subtotal_cents, starts_at, ends_at, usage_limit, status, applies_to, requires_code, eligible_tag)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, code, type, value_percent, value_cents, min_subtotal_cents,
			starts_at, ends_at, usage_limit, times_used, status, created_at,
			applies_to, buy_quantity, get_quantity, requires_code, eligible_tag`,
		tenantID(c), code, req.Type, req.ValuePercent, req.ValueCents, req.MinSubtotalCents,
		startsAt, endsAt, req.UsageLimit, status, appliesTo, requiresCode, eligibleTag))
	if err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "discount code already exists")
		}
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(toJSON(d))
}

func ptr[T any](v T) *T { return &v }

func orEmpty(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// GetDiscount handles GET /discounts/:id (Admin).
func (s *Service) GetDiscount(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	d, err := scanDiscount(tx.QueryRow(c.Context(),
		discountSelect+" WHERE id = $1 AND tenant_id = $2", c.Params("id"), tenantID(c)))
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
	cur, err := scanDiscount(tx.QueryRow(ctx,
		discountSelect+" WHERE id = $1 AND tenant_id = $2 FOR SHARE", c.Params("id"), tenantID(c)))
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
	appliesTo, requiresCode := cur.appliesTo, cur.requiresCode
	buyQuantity, getQuantity := cur.buyQuantity, cur.getQuantity
	eligibleTag := cur.eligibleTag
	if v := strings.TrimSpace(req.Code); v != "" {
		code = ptr(strings.ToUpper(v))
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
	if req.AppliesTo != "" {
		appliesTo = req.AppliesTo
	}
	if req.RequiresCode != nil {
		requiresCode = *req.RequiresCode
	}
	if req.BuyQuantity != nil {
		buyQuantity = req.BuyQuantity
	}
	if req.GetQuantity != nil {
		getQuantity = req.GetQuantity
	}
	if req.EligibleTag != nil {
		eligibleTag = normalizeEligibleTag(req.EligibleTag)
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
	if err := validateFields(orEmpty(code), dType, valuePercent, valueCents, minSubtotal, usageLimit,
		requiresCode, appliesTo, buyQuantity, getQuantity); err != nil {
		return translate(err)
	}
	if status != "active" && status != "disabled" {
		return httperr.C(fiber.StatusBadRequest, "status must be active or disabled")
	}

	d, err := scanDiscount(tx.QueryRow(ctx, `
		UPDATE discounts SET code = $1, type = $2, value_percent = $3, value_cents = $4,
			min_subtotal_cents = $5, starts_at = $6, ends_at = $7, usage_limit = $8, status = $9,
			applies_to = $10, requires_code = $11, eligible_tag = $12
		WHERE id = $13 AND tenant_id = $14
		RETURNING id, code, type, value_percent, value_cents, min_subtotal_cents,
			starts_at, ends_at, usage_limit, times_used, status, created_at,
			applies_to, buy_quantity, get_quantity, requires_code, eligible_tag`,
		code, dType, valuePercent, valueCents, minSubtotal, start, end, usageLimit, status,
		appliesTo, requiresCode, eligibleTag, c.Params("id"), tenantID(c)))
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
	tag, err := tx.Exec(c.Context(), "DELETE FROM discounts WHERE id = $1 AND tenant_id = $2",
		c.Params("id"), tenantID(c))
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

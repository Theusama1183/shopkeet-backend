package cart

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/discounts"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service implements the guest cart surface. Handlers read/write through the
// request transaction CustomerMW opened (c.Locals("tx")), so RLS scopes every
// query to the resolved tenant; within it, cart lookup is keyed by the opaque
// customer_session.
type Service struct {
	pool     *pgxpool.Pool
	reserver Reserver
}

// New builds a cart Service. reserver is best-effort only (see reserve.go).
func New(pool *pgxpool.Pool, reserver Reserver) *Service {
	return &Service{pool: pool, reserver: reserver}
}

// --- helpers ------------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func customerSession(c *fiber.Ctx) string {
	s, _ := c.Locals("customer_session").(string)
	return s
}

type itemRow struct {
	id             string
	productID      string
	variantID      string
	name           string
	slug           string
	quantity       int
	priceCents     int
	currency       string
	lineTotalCents int
}

type cartPayload struct {
	id            string
	items         []itemRow
	total         int
	currency      string
	discountCode  string
	discountCents int
}

// loadCart returns the customer's cart with its items (product info joined in)
// and totals, plus the applied discount_code and its predicted discount_cents
// (best-effort — checkout re-validates authoritatively). Returns (nil, nil)
// when the session has no cart yet.
func loadCart(c *fiber.Ctx, tx pgx.Tx, session string) (*cartPayload, error) {
	ctx := c.Context()
	var cartID, discountCode string
	err := tx.QueryRow(ctx,
		"SELECT id, COALESCE(discount_code, '') FROM carts WHERE customer_session = $1",
		session).Scan(&cartID, &discountCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT ci.id, ci.product_id, ci.variant_id, ci.quantity,
		       p.name, p.slug, v.price_cents, p.currency
		FROM cart_items ci
		JOIN products p ON p.id = ci.product_id
		JOIN product_variants v ON v.id = ci.variant_id
		WHERE ci.cart_id = $1
		ORDER BY p.name`, cartID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cp := &cartPayload{id: cartID}
	for rows.Next() {
		var it itemRow
		if err := rows.Scan(&it.id, &it.productID, &it.variantID, &it.quantity,
			&it.name, &it.slug, &it.priceCents, &it.currency); err != nil {
			return nil, err
		}
		it.lineTotalCents = it.quantity * it.priceCents
		cp.items = append(cp.items, it)
		cp.total += it.lineTotalCents
		cp.currency = it.currency
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	cp.discountCode = discountCode
	if discountCode != "" {
		if q, err := discounts.Resolve(ctx, tx, discountCode, cp.total); err == nil {
			cp.discountCents = q.DiscountCents
		}
	}
	return cp, nil
}

func cartJSON(cp *cartPayload) fiber.Map {
	if cp == nil {
		return fiber.Map{"cart": nil}
	}
	items := make([]fiber.Map, 0, len(cp.items))
	for _, it := range cp.items {
		items = append(items, fiber.Map{
			"id":               it.id,
			"product_id":       it.productID,
			"variant_id":       it.variantID,
			"name":             it.name,
			"slug":             it.slug,
			"quantity":         it.quantity,
			"price_cents":      it.priceCents,
			"currency":         it.currency,
			"line_total_cents": it.lineTotalCents,
		})
	}
	return fiber.Map{"cart": fiber.Map{
		"id":             cp.id,
		"items":          items,
		"total_cents":    cp.total,
		"currency":       cp.currency,
		"discount_code":  cp.discountCode,
		"discount_cents": cp.discountCents,
	}}
}

// --- handlers -------------------------------------------------------------------

// GetCart handles GET /cart (Customer). Returns {"cart": null} when this
// session has no cart yet — a fresh guest always has a valid empty cart.
func (s *Service) GetCart(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

type addItemRequest struct {
	VariantID string `json:"variant_id"`
	Quantity  int    `json:"quantity"`
}

// AddItem handles POST /cart (Customer). Creates the cart on first use and
// merges quantity for the same variant already in the cart. Only variants of
// active products can be carted. Stock is deliberately NOT gated here —
// checkout arbitrates (docs/07-expansion-build-spec.md Phase 8). Best-effort
// Redis reserve on the way out.
func (s *Service) AddItem(c *fiber.Ctx) error {
	var req addItemRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.VariantID == "" || req.Quantity < 1 || req.Quantity > 999 {
		return httperr.C(fiber.StatusBadRequest, "variant_id and quantity (1-999) required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	// Variant must exist, belong to an active product, and itself be active
	// (RLS-scoped by the request tenant).
	var productID string
	var active bool
	err := tx.QueryRow(ctx, `
		SELECT v.product_id, (p.status = 'active' AND v.status = 'active')
		FROM product_variants v
		JOIN products p ON p.id = v.product_id
		WHERE v.id = $1`, req.VariantID).Scan(&productID, &active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return httperr.C(fiber.StatusNotFound, "variant not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	// One cart per session (UNIQUE (tenant_id, customer_session)); merge lines.
	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	var cartID string
	if err := tx.QueryRow(ctx,
		"SELECT id FROM carts WHERE customer_session = $1", session).Scan(&cartID); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cart_items (tenant_id, cart_id, product_id, variant_id, quantity)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (cart_id, variant_id)
		DO UPDATE SET quantity = cart_items.quantity + EXCLUDED.quantity`,
		tid, cartID, productID, req.VariantID, req.Quantity); err != nil {
		return httperr.ErrInternalServerError
	}

	// Fast-path reservation; never fails the request (checkout is authoritative).
	_ = s.reserver.ReserveUnits(ctx, req.VariantID, req.Quantity)

	cp, err := loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

type quantityRequest struct {
	Quantity int `json:"quantity"`
}

// UpdateItemQuantity handles PATCH /cart/items/:id (Customer).
func (s *Service) UpdateItemQuantity(c *fiber.Ctx) error {
	var req quantityRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Quantity < 1 || req.Quantity > 999 {
		return httperr.C(fiber.StatusBadRequest, "quantity (1-999) required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), `
		UPDATE cart_items ci SET quantity = $1
		FROM carts c
		WHERE ci.id = $2 AND ci.cart_id = c.id AND c.customer_session = $3`,
		req.Quantity, c.Params("id"), customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "cart item not found")
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

// RemoveItem handles DELETE /cart/items/:id (Customer).
func (s *Service) RemoveItem(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), `
		DELETE FROM cart_items ci
		USING carts c
		WHERE ci.id = $1 AND ci.cart_id = c.id AND c.customer_session = $2`,
		c.Params("id"), customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "cart item not found")
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

type discountRequest struct {
	Code string `json:"code"`
}

// ApplyDiscount handles POST /cart/discount (Customer). Validates the code at
// apply-time (exists, active, in date range, subtotal minimum, usage headroom)
// via discounts.Resolve and stores it on the cart. Checkout re-validates and
// claims the usage inside the order transaction — the apply here is never
// authoritative.
func (s *Service) ApplyDiscount(c *fiber.Ctx) error {
	var req discountRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		return httperr.C(fiber.StatusBadRequest, "code required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	// The cart may not exist yet (code applied before the first item) — the
	// subtotal is then 0, and any minimum still gates the apply.
	cp, err := loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	subtotal := 0
	if cp != nil {
		subtotal = cp.total
	}
	if _, err := discounts.Resolve(ctx, tx, code, subtotal); err != nil {
		return translateDiscountErr(err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx,
		"UPDATE carts SET discount_code = $1 WHERE customer_session = $2", code, session); err != nil {
		return httperr.ErrInternalServerError
	}
	cp, err = loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

// translateDiscountErr maps a discounts.CodeError to the JSON error shape; any
// unexpected error is a 500.
func translateDiscountErr(err error) error {
	var ce *discounts.CodeError
	if errors.As(err, &ce) {
		return httperr.C(ce.Status, ce.Message)
	}
	return httperr.ErrInternalServerError
}

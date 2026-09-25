package orders

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/discounts"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/shipping"
)

// Service implements checkout + order management for guest Cash-on-Delivery
// purchases. All handlers execute inside the RLS-scoped request transaction
// opened by CustomerMW (checkout/lookup) or TenantMW (admin list/status), so
// every query is bound to the resolved tenant.
type Service struct {
	pool     *pgxpool.Pool
	bus      *events.Bus
	payments *payments.Registry
}

// New builds an orders Service. The event bus receives order.created /
// order.paid; the registry holds the payment providers (v1: cod only).
func New(pool *pgxpool.Pool, bus *events.Bus, reg *payments.Registry) *Service {
	return &Service{pool: pool, bus: bus, payments: reg}
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

type orderItemRow struct {
	id             string
	productID      string
	variantID      string
	quantity       int
	unitPriceCents int
}

type orderRow struct {
	id                string
	customerID        *string // Phase 11: set when checkout ran under a customer JWT; NULL for guests
	customerName      string
	customerPhone     string
	customerEmail     *string
	shippingAddress   *string // deprecated flat column — historical only; no longer written
	line1             *string
	line2             *string
	city              *string
	state             *string
	postalCode        *string
	country           *string
	shippingMethod    *string
	shippingCostCents int
	discountCode      *string
	discountCents     int
	taxCents          int     // Phase 13
	internalNote      *string // Phase 13: merchant-only, not shown to customers
	paymentMethod     string
	paymentStatus     string
	status            string
	totalCents        int
	currency          string
	createdAt         time.Time
	items             []orderItemRow
}

const orderSelect = `
	SELECT id, customer_id, customer_name, customer_phone, customer_email, shipping_address,
	       shipping_address_line1, shipping_address_line2, shipping_city, shipping_state,
	       shipping_postal_code, shipping_country, shipping_method, shipping_cost_cents,
	       discount_code, discount_cents,
	       tax_cents, internal_note,
	       payment_method, payment_status, status, total_cents, currency, created_at
	FROM orders`

// loadOrder hydrates one order plus its items.
func loadOrder(c *fiber.Ctx, tx pgx.Tx, where string, args ...any) (*orderRow, error) {
	ctx := c.Context()
	var o orderRow
	err := tx.QueryRow(ctx, orderSelect+" WHERE "+where, args...).
		Scan(&o.id, &o.customerID, &o.customerName, &o.customerPhone, &o.customerEmail, &o.shippingAddress,
			&o.line1, &o.line2, &o.city, &o.state, &o.postalCode, &o.country,
			&o.shippingMethod, &o.shippingCostCents, &o.discountCode, &o.discountCents,
			&o.taxCents, &o.internalNote,
			&o.paymentMethod, &o.paymentStatus, &o.status, &o.totalCents, &o.currency, &o.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id, product_id, variant_id, quantity, unit_price_cents
		FROM order_items
		WHERE order_id = $1
		ORDER BY id`, o.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it orderItemRow
		if err := rows.Scan(&it.id, &it.productID, &it.variantID, &it.quantity, &it.unitPriceCents); err != nil {
			return nil, err
		}
		o.items = append(o.items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return &o, nil
}

func strp(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func orderJSON(o *orderRow, includeInternalNote bool) fiber.Map {
	items := make([]fiber.Map, 0, len(o.items))
	for _, it := range o.items {
		items = append(items, fiber.Map{
			"id": it.id, "product_id": it.productID, "variant_id": it.variantID,
			"quantity":         it.quantity,
			"unit_price_cents": it.unitPriceCents,
			"line_total_cents": it.quantity * it.unitPriceCents,
		})
	}
	m := fiber.Map{
		"id": o.id, "customer_id": strp(o.customerID),
		"customer_name": o.customerName, "customer_phone": o.customerPhone,
		"customer_email": strp(o.customerEmail), "shipping_address": strp(o.shippingAddress),
		"shipping_address_line1": strp(o.line1), "shipping_address_line2": strp(o.line2),
		"shipping_city": strp(o.city), "shipping_state": strp(o.state),
		"shipping_postal_code": strp(o.postalCode), "shipping_country": strp(o.country),
		"shipping_method": strp(o.shippingMethod), "shipping_cost_cents": o.shippingCostCents,
		"discount_code": strp(o.discountCode), "discount_cents": o.discountCents,
		"tax_cents":      o.taxCents,
		"payment_method": o.paymentMethod, "payment_status": o.paymentStatus,
		"status": o.status, "total_cents": o.totalCents, "currency": o.currency,
		"created_at": o.createdAt.Format(time.RFC3339), "items": items,
	}
	if includeInternalNote {
		m["internal_note"] = strp(o.internalNote)
	}
	return m
}

// --- checkout -----------------------------------------------------------------

type checkoutRequest struct {
	CustomerName         string `json:"customer_name"`
	CustomerPhone        string `json:"customer_phone"`
	CustomerEmail        string `json:"customer_email"`
	ShippingAddress      string `json:"shipping_address"` // legacy input; deprecated — not written anymore
	ShippingAddressLine1 string `json:"shipping_address_line1"`
	ShippingAddressLine2 string `json:"shipping_address_line2"`
	ShippingCity         string `json:"shipping_city"`
	ShippingState        string `json:"shipping_state"`
	ShippingPostalCode   string `json:"shipping_postal_code"`
	ShippingCountry      string `json:"shipping_country"`
	ShippingRateID       string `json:"shipping_rate_id"`
	PaymentMethod        string `json:"payment_method"`
}

// Checkout handles POST /checkout (Customer). Runs inside the request
// transaction: locks product rows FOR UPDATE (the authoritative anti-oversell
// guard), rejects lines that can't be fulfilled, snapshots unit prices, resolves
// the chosen shipping rate against the destination (free-over may waive the
// cost), creates the order, decrements inventory, clears the cart, and emits
// order.created.
func (s *Service) Checkout(c *fiber.Ctx) error {
	var req checkoutRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.CustomerName == "" || req.CustomerPhone == "" {
		return httperr.C(fiber.StatusBadRequest, "customer_name, customer_phone required")
	}
	if req.ShippingAddressLine1 == "" || req.ShippingCity == "" || req.ShippingCountry == "" {
		return httperr.C(fiber.StatusBadRequest,
			"shipping_address_line1, shipping_city, shipping_country required")
	}
	if req.ShippingRateID == "" {
		return httperr.C(fiber.StatusBadRequest, "shipping_rate_id required")
	}
	if req.PaymentMethod == "" {
		req.PaymentMethod = "cod"
	}
	provider, ok := s.payments.Get(req.PaymentMethod)
	if !ok {
		return httperr.C(fiber.StatusBadRequest, "unsupported payment method")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	// Phase 11: when checkout runs under a customer JWT (CustomerOrGuestMW) the
	// order is linked to the account; guests stay NULL.
	var customerID *string
	if cid, ok := c.Locals("customer_id").(string); ok && cid != "" {
		customerID = &cid
	}

	var cartID, discountCode string
	if err := tx.QueryRow(ctx,
		"SELECT id, COALESCE(discount_code, '') FROM carts WHERE customer_session = $1",
		session).Scan(&cartID, &discountCode); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusBadRequest, "cart is empty")
	}

	// Lock every variant in the cart (plus its product row for the status
	// check). This is the real guard against overselling: two concurrent
	// checkouts for the last unit serialize here, and the second sees the
	// already-decremented inventory.
	rows, err := tx.Query(ctx, `
		SELECT ci.variant_id, ci.product_id, ci.quantity, v.price_cents, p.currency,
		       v.inventory_count, v.status, p.status
		FROM cart_items ci
		JOIN product_variants v ON v.id = ci.variant_id
		JOIN products p ON p.id = v.product_id
		WHERE ci.cart_id = $1
		ORDER BY v.id
		FOR UPDATE OF v, p`, cartID)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	type line struct {
		variantID string
		productID string
		quantity  int
		price     int
		currency  string
	}
	var lines []line
	for rows.Next() {
		var l line
		var inventory int
		var variantStatus, productStatus string
		if err := rows.Scan(&l.variantID, &l.productID, &l.quantity, &l.price, &l.currency,
			&inventory, &variantStatus, &productStatus); err != nil {
			rows.Close()
			return httperr.ErrInternalServerError
		}
		if variantStatus != "active" || productStatus != "active" {
			rows.Close()
			return httperr.C(fiber.StatusConflict, "a product in your cart is no longer available")
		}
		if inventory < l.quantity {
			rows.Close()
			return httperr.C(fiber.StatusConflict, "insufficient stock")
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()
	if len(lines) == 0 {
		return httperr.C(fiber.StatusBadRequest, "cart is empty")
	}

	res, err := provider.Process(ctx, payments.ProcessRequest{
		Method: req.PaymentMethod, AmountCents: 0, Currency: "usd"})
	if err != nil {
		return httperr.ErrInternalServerError
	}

	subtotal := 0
	currency := lines[0].currency
	for _, l := range lines {
		subtotal += l.quantity * l.price
	}

	// Fetch tenant's tax rate (Phase 13).
	var taxRatePercent int
	if err := tx.QueryRow(ctx, `SELECT tax_rate_percent FROM tenants WHERE id = $1`, tid).
		Scan(&taxRatePercent); err != nil {
		taxRatePercent = 0
	}

	// Resolve the chosen shipping rate against the destination. The applied cost
	// (rate_cents, or 0 when the subtotal clears free_over_cents) and the rate
	// name are snapshotted into the order — later rate edits never touch it.
	quote, err := shipping.ResolveRate(ctx, tx, req.ShippingRateID,
		req.ShippingCountry, req.ShippingState, subtotal)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if quote == nil {
		return httperr.C(fiber.StatusBadRequest,
			"shipping rate does not match the destination")
	}

	// Apply the applied discount code (Phase 10): re-validated inside this
	// transaction and claimed via FOR UPDATE, so expired / over-limit / deleted
	// codes are rejected at checkout even when they were fine when applied to
	// the cart earlier, and concurrent checkouts never over-consume a cap.
	discountCents := 0
	if discountCode != "" {
		q, err := discounts.Claim(ctx, tx, discountCode, subtotal)
		if err != nil {
			var ce *discounts.CodeError
			if errors.As(err, &ce) {
				return httperr.C(ce.Status, ce.Message)
			}
			return httperr.ErrInternalServerError
		}
		discountCents = q.DiscountCents
	}

	// Tax is calculated on the subtotal (Phase 13).
	taxCents := subtotal * taxRatePercent / 100
	total := subtotal - discountCents + quote.CostCents + taxCents

	var orderID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO orders (tenant_id, customer_id, customer_name, customer_phone, customer_email,
			shipping_address_line1, shipping_address_line2, shipping_city, shipping_state,
			shipping_postal_code, shipping_country, shipping_method, shipping_cost_cents,
			discount_code, discount_cents, tax_cents,
			payment_method, payment_status, status, total_cents, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, 'pending', $19, $20)
		RETURNING id`,
		tid, customerID, req.CustomerName, req.CustomerPhone, nullableStr(req.CustomerEmail),
		nullableStr(req.ShippingAddressLine1), nullableStr(req.ShippingAddressLine2),
		nullableStr(req.ShippingCity), nullableStr(req.ShippingState),
		nullableStr(req.ShippingPostalCode), nullableStr(req.ShippingCountry),
		nullableStr(quote.Method), quote.CostCents,
		nullableStr(discountCode), discountCents, taxCents,
		req.PaymentMethod, res.PaymentStatus, total, currency).Scan(&orderID); err != nil {
		return httperr.ErrInternalServerError
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (tenant_id, order_id, product_id, variant_id, quantity, unit_price_cents)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			tid, orderID, l.productID, l.variantID, l.quantity, l.price); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx,
			"UPDATE product_variants SET inventory_count = inventory_count - $1 WHERE id = $2",
			l.quantity, l.variantID); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	// Refresh the cached products.price_cents / products.inventory_count
	// aggregates after the variant decrements.
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			UPDATE products p SET
				price_cents = COALESCE((SELECT MIN(price_cents) FROM product_variants v
					WHERE v.product_id = p.id AND v.status = 'active'), 0),
				inventory_count = COALESCE((SELECT SUM(inventory_count) FROM product_variants v
					WHERE v.product_id = p.id AND v.status = 'active'), 0)
			WHERE p.id = $1`, l.productID); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM cart_items WHERE cart_id = $1", cartID); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM carts WHERE id = $1", cartID); err != nil {
		return httperr.ErrInternalServerError
	}

	if err := s.bus.Emit(ctx, events.Event{
		Name: "order.created",
		Data: fiber.Map{"order_id": orderID, "tenant_id": tid},
	}); err != nil {
		return httperr.ErrInternalServerError
	}

	order, err := loadOrder(c, tx, "id = $1", orderID)
	if err != nil || order == nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(orderJSON(order, false))
}

func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- customer order lookup -----------------------------------------------------

// GetOrder handles GET /orders/:id (Customer). Verified by phone (required)
// and, if provided, email in query params — a random order id alone reveals
// nothing, and the tenant header + RLS scope the lookup to this store.
func (s *Service) GetOrder(c *fiber.Ctx) error {
	phone := c.Query("phone")
	if phone == "" {
		return httperr.C(fiber.StatusBadRequest, "phone query param required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	var order *orderRow
	var err error
	if email := c.Query("email"); email != "" {
		order, err = loadOrder(c, tx,
			"id = $1 AND customer_phone = $2 AND customer_email = $3",
			c.Params("id"), phone, email)
	} else {
		order, err = loadOrder(c, tx, "id = $1 AND customer_phone = $2",
			c.Params("id"), phone)
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if order == nil {
		return httperr.C(fiber.StatusNotFound, "order not found")
	}
	return c.JSON(orderJSON(order, false))
}

// --- admin: list & status ------------------------------------------------------

// ListOrders handles GET /orders (Admin). Returns the tenant's orders, newest
// first, optionally filtered by ?status=, each with its items.
func (s *Service) ListOrders(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	where := "1 = 1"
	var args []any
	if st := c.Query("status"); st != "" {
		where = "status = $1"
		args = append(args, st)
	}
	rows, err := tx.Query(ctx, orderSelect+" WHERE "+where+" ORDER BY created_at DESC, id", args...)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	var found []orderRow
	for rows.Next() {
		var o orderRow
		if err := rows.Scan(&o.id, &o.customerID, &o.customerName, &o.customerPhone, &o.customerEmail, &o.shippingAddress,
			&o.line1, &o.line2, &o.city, &o.state, &o.postalCode, &o.country,
			&o.shippingMethod, &o.shippingCostCents, &o.discountCode, &o.discountCents,
			&o.taxCents, &o.internalNote,
			&o.paymentMethod, &o.paymentStatus, &o.status, &o.totalCents, &o.currency, &o.createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		found = append(found, o)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()

	// Hydrate items after the cursor is closed (pgx refuses a second query on
	// an open connection otherwise).
	ordersJSON := make([]fiber.Map, 0, len(found))
	for i := range found {
		var items []orderItemRow
		irows, err := tx.Query(ctx, `
			SELECT id, product_id, variant_id, quantity, unit_price_cents
			FROM order_items WHERE order_id = $1 ORDER BY id`, found[i].id)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		for irows.Next() {
			var it orderItemRow
			if err := irows.Scan(&it.id, &it.productID, &it.variantID, &it.quantity, &it.unitPriceCents); err != nil {
				irows.Close()
				return httperr.ErrInternalServerError
			}
			items = append(items, it)
		}
		if err := irows.Err(); err != nil {
			return httperr.ErrInternalServerError
		}
		irows.Close()
		found[i].items = items
		ordersJSON = append(ordersJSON, orderJSON(&found[i], true))
	}
	return c.JSON(fiber.Map{"orders": ordersJSON})
}

type updateStatusRequest struct {
	Status string `json:"status"`
}

// nextStatus is the forward chain; cancelled is handled separately below.
var nextStatus = map[string]string{
	"pending":   "confirmed",
	"confirmed": "shipped",
	"shipped":   "delivered",
}

// UpdateStatus handles PATCH /orders/:id/status (Admin). Advances the order
// pending → confirmed → shipped → delivered; delivered also sets
// payment_status="paid" and emits order.paid. pending/confirmed may instead be
// cancelled.
func (s *Service) UpdateStatus(c *fiber.Ctx) error {
	var req updateStatusRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	var current string
	if err := tx.QueryRow(ctx,
		"SELECT status FROM orders WHERE id = $1 FOR UPDATE", c.Params("id")).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "order not found")
	}

	target := req.Status
	var sets string
	switch target {
	case "cancelled":
		if current != "pending" && current != "confirmed" {
			return httperr.C(fiber.StatusBadRequest, "invalid status transition")
		}
		sets = "status = 'cancelled'"
	case "confirmed", "shipped", "delivered":
		if nextStatus[current] != target {
			return httperr.C(fiber.StatusBadRequest, "invalid status transition")
		}
		sets = "status = '" + target + "'"
		if target == "delivered" {
			sets += ", payment_status = 'paid'"
		}
	default:
		return httperr.C(fiber.StatusBadRequest, "invalid status")
	}

	if _, err := tx.Exec(ctx,
		"UPDATE orders SET "+sets+" WHERE id = $1", c.Params("id")); err != nil {
		return httperr.ErrInternalServerError
	}

	if target == "delivered" {
		tid, _ := c.Locals("tenant_id").(string)
		if err := s.bus.Emit(ctx, events.Event{
			Name: "order.paid",
			Data: fiber.Map{"order_id": c.Params("id"), "tenant_id": tid},
		}); err != nil {
			return httperr.ErrInternalServerError
		}
	}

	order, err := loadOrder(c, tx, "id = $1", c.Params("id"))
	if err != nil || order == nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(orderJSON(order, true))
}

// UpdateNote handles PATCH /orders/:id/note (Admin). Sets or clears the
// merchant-only internal note on an order. The note is never shown to the
// customer (excluded from customer-facing responses).
func (s *Service) UpdateNote(c *fiber.Ctx) error {
	var req struct {
		Note *string `json:"note"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}

	tx, ok := c.Locals("tx").(pgx.Tx)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	if _, err := tx.Exec(ctx, `UPDATE orders SET internal_note = $1 WHERE id = $2`,
		req.Note, c.Params("id")); err != nil {
		return httperr.ErrInternalServerError
	}

	order, err := loadOrder(c, tx, "id = $1", c.Params("id"))
	if err != nil || order == nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(orderJSON(order, true))
}

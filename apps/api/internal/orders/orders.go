package orders

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/bundles"
	"github.com/shopkeet/api/internal/discounts"
	"github.com/shopkeet/api/internal/giftcards"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/cache"
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
	cache    cache.Cache
}

// New builds an orders Service. The event bus receives order.created /
// order.paid; the registry holds the payment providers (v1: cod only).
// Caching is disabled until SetCache is called.
func New(pool *pgxpool.Pool, bus *events.Bus, reg *payments.Registry) *Service {
	return &Service{pool: pool, bus: bus, payments: reg, cache: cache.Noop{}}
}

// SetCache enables product-detail cache invalidation after checkout: the
// inventory decrement refreshes product_variants and the products aggregates,
// so any cached public product JSON (which embeds inventory_count) must be
// dropped for the affected product.
func (s *Service) SetCache(c cache.Cache) {
	if c != nil {
		s.cache = c
	}
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
	bundleID       *string // Phase 25 — set when the line belonged to a bundle
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
	giftCardCode      *string // Phase 18
	giftCardCents     int     // Phase 18
	taxCents          int     // Phase 13
	internalNote      *string // Phase 13: merchant-only, not shown to customers
	paymentMethod     string
	paymentStatus     string
	status            string
	source            string // 'storefront' (checkout) | 'draft' (merchant-created, Phase 15)
	totalCents        int
	currency          string
	trackingNumber    *string // Phase 23
	trackingCarrier   *string // Phase 23
	trackingURL       *string // Phase 23
	createdAt         time.Time
	items             []orderItemRow
}

const orderSelect = `
	SELECT id, customer_id, customer_name, customer_phone, customer_email, shipping_address,
	       shipping_address_line1, shipping_address_line2, shipping_city, shipping_state,
	       shipping_postal_code, shipping_country, shipping_method, shipping_cost_cents,
	       discount_code, discount_cents, gift_card_code, gift_card_cents,
	       tax_cents, internal_note,
	       payment_method, payment_status, status, source, total_cents, currency,
	       tracking_number, tracking_carrier, tracking_url, created_at
	FROM orders`

// loadOrder hydrates one order plus its items.
func loadOrder(c *fiber.Ctx, tx pgx.Tx, where string, args ...any) (*orderRow, error) {
	ctx := c.Context()
	var o orderRow
	err := tx.QueryRow(ctx, orderSelect+" WHERE "+where, args...).
		Scan(&o.id, &o.customerID, &o.customerName, &o.customerPhone, &o.customerEmail, &o.shippingAddress,
			&o.line1, &o.line2, &o.city, &o.state, &o.postalCode, &o.country,
			&o.shippingMethod, &o.shippingCostCents, &o.discountCode, &o.discountCents,
			&o.giftCardCode, &o.giftCardCents,
			&o.taxCents, &o.internalNote,
			&o.paymentMethod, &o.paymentStatus, &o.status, &o.source, &o.totalCents, &o.currency,
			&o.trackingNumber, &o.trackingCarrier, &o.trackingURL, &o.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id, product_id, variant_id, quantity, unit_price_cents, bundle_id
		FROM order_items
		WHERE order_id = $1
		ORDER BY id`, o.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it orderItemRow
		if err := rows.Scan(&it.id, &it.productID, &it.variantID, &it.quantity, &it.unitPriceCents, &it.bundleID); err != nil {
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
			"bundle_id":        strp(it.bundleID),
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
		"gift_card_code": strp(o.giftCardCode), "gift_card_cents": o.giftCardCents,
		"tax_cents":      o.taxCents,
		"payment_method": o.paymentMethod, "payment_status": o.paymentStatus,
		"status": o.status, "source": o.source, "total_cents": o.totalCents, "currency": o.currency,
		"tracking_number": strp(o.trackingNumber), "tracking_carrier": strp(o.trackingCarrier),
		"tracking_url": strp(o.trackingURL),
		"created_at":   o.createdAt.Format(time.RFC3339), "items": items,
	}
	if includeInternalNote {
		m["internal_note"] = strp(o.internalNote)
	}
	return m
}

// --- checkout -----------------------------------------------------------------

// line is one locked cart row during checkout (its prices and availability are
// authoritative snapshots for the order).
type line struct {
	cartItemID    string
	bundleID      *string
	variantID     string
	productID     string
	quantity      int
	price         int
	currency      string
	allowPreorder bool
	preorder      bool
}

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

	var cartID, discountCode, giftCardCode string
	if err := tx.QueryRow(ctx,
		`SELECT id, COALESCE(discount_code, ''), COALESCE(gift_card_code, '')
		FROM carts WHERE tenant_id = $1 AND customer_session = $2`,
		tid, session).Scan(&cartID, &discountCode, &giftCardCode); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusBadRequest, "cart is empty")
	}

	// Lock every variant in the cart (plus its product row for the status
	// check). This is the real guard against overselling: two concurrent
	// checkouts for the last unit serialize here, and the second sees the
	// already-decremented inventory.
	rows, err := tx.Query(ctx, `
		SELECT ci.id, ci.bundle_id, ci.variant_id, ci.product_id, ci.quantity, v.price_cents, p.currency,
		       v.inventory_count, v.status, p.status, v.allow_preorder
		FROM cart_items ci
		JOIN product_variants v ON v.id = ci.variant_id
		JOIN products p ON p.id = v.product_id
		WHERE ci.cart_id = $1 AND ci.tenant_id = $2
		ORDER BY v.id
		FOR UPDATE OF v, p`, cartID, tid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	var lines []line
	for rows.Next() {
		var l line
		var inventory int
		var variantStatus, productStatus string
		if err := rows.Scan(&l.cartItemID, &l.bundleID, &l.variantID, &l.productID, &l.quantity, &l.price, &l.currency,
			&inventory, &variantStatus, &productStatus, &l.allowPreorder); err != nil {
			rows.Close()
			return httperr.ErrInternalServerError
		}
		if variantStatus != "active" || productStatus != "active" {
			rows.Close()
			return httperr.C(fiber.StatusConflict, "a product in your cart is no longer available")
		}
		// Phase 19 — a preorderable variant may sell beyond its current stock.
		// The whole line becomes a preorder (is_preorder=true, no inventory
		// decrement) only when available stock can't cover it; otherwise it's
		// a normal sale.
		l.preorder = l.allowPreorder && inventory < l.quantity
		if !l.preorder && inventory < l.quantity {
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

	// Phase 25 — bundles must still be purchasable at checkout. A bundle set to
	// draft/archived (or deleted) between add-to-cart and checkout rejects the
	// whole checkout, mirroring the unavailable-product gate above.
	if ids := distinctBundleIDs(lines); len(ids) > 0 {
		n, err := tx.Query(ctx, `
			SELECT id FROM bundles WHERE id = ANY($1) AND status = 'active'`, ids)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		var activeCount int
		for n.Next() {
			activeCount++
		}
		if err := n.Err(); err != nil {
			n.Close()
			return httperr.ErrInternalServerError
		}
		n.Close()
		if activeCount != len(ids) {
			return httperr.C(fiber.StatusConflict, "a bundle in your cart is no longer available")
		}
	}

	res, err := provider.Process(ctx, payments.ProcessRequest{
		Method: req.PaymentMethod, AmountCents: 0, Currency: "usd"})
	if err != nil {
		return httperr.ErrInternalServerError
	}

	// Phase 25 — the goods subtotal is repriced exactly like the cart preview:
	// bundle rows charge as a unit (flat bundle price, or the configured % off
	// the summed components), plain rows get the best qualifying quantity-break
	// discount. The real component prices are still snapshotted into
	// order_items and stock is still decremented per component variant.
	cartLines := make([]bundles.CartItem, 0, len(lines))
	for _, l := range lines {
		cartLines = append(cartLines, bundles.CartItem{
			CartItemID: l.cartItemID, ProductID: l.productID, VariantID: l.variantID,
			Quantity: l.quantity, PriceCents: l.price, BundleID: l.bundleID,
		})
	}
	_, subtotal, err := bundles.PriceCart(ctx, tx, cartLines)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	currency := lines[0].currency

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
	if errors.Is(err, shipping.ErrStateRequired) {
		return httperr.C(fiber.StatusBadRequest, "shipping_state required for this destination")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if quote == nil {
		return httperr.C(fiber.StatusBadRequest,
			"shipping rate does not match the destination")
	}

	// Apply discounts (Phases 10 + 21). The entered code is validated read-only
	// first, the best automatic discount (requires_code=false) is picked under
	// FOR UPDATE locks, and v1 applies exactly one of them — whichever saves the
	// shopper more — so a code and a store-wide promo never stack. Only the
	// winner is claimed (times_used bumped), so a beaten code is never burned.
	// Codes are re-validated inside this transaction, so an expired / over-limit
	// / deleted code is rejected at checkout even when it applied fine to the
	// cart earlier.
	codeCents := 0
	if discountCode != "" {
		q, err := discounts.Resolve(ctx, tx, tid, discountCode, subtotal)
		if err != nil {
			var ce *discounts.CodeError
			if errors.As(err, &ce) {
				return httperr.C(ce.Status, ce.Message)
			}
			return httperr.ErrInternalServerError
		}
		codeCents = q.DiscountCents
	}
	auto, err := discounts.AutoPick(ctx, tx, tid, subtotal, quote.CostCents)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	// The winner reduces the goods subtotal (order scope) or the shipping cost
	// (shipping scope) — never both, so tax is computed on the true taxable
	// base. An automatic shipping discount must not shrink the taxable subtotal,
	// and the order keeps the original rate cost in shipping_cost_cents while
	// discount_cents records the total savings (goods + shipping).
	discountCents, shippingDiscountCents := 0, 0
	if auto != nil && auto.DiscountCents > codeCents {
		claimed, err := discounts.ClaimAuto(ctx, tx, tid, auto.ID, subtotal, quote.CostCents)
		if err != nil {
			var ce *discounts.CodeError
			if errors.As(err, &ce) {
				return httperr.C(ce.Status, ce.Message)
			}
			return httperr.ErrInternalServerError
		}
		if claimed.AppliesTo == "shipping" {
			shippingDiscountCents = claimed.DiscountCents
		} else {
			discountCents = claimed.DiscountCents
		}
		discountCode = "" // automatic — nothing to snapshot as a code
	} else if codeCents > 0 {
		q, err := discounts.Claim(ctx, tx, tid, discountCode, subtotal)
		if err != nil {
			var ce *discounts.CodeError
			if errors.As(err, &ce) {
				return httperr.C(ce.Status, ce.Message)
			}
			return httperr.ErrInternalServerError
		}
		discountCents = q.DiscountCents
	}
	// goodsDiscount reduces the taxable base; the shipping discount cuts only
	// the shipping cost. discount_cents on the order records the total savings
	// (goods + shipping) while shipping_cost_cents keeps the original rate.
	goodsDiscount := discountCents
	discountCents += shippingDiscountCents

	// Tax is calculated on the discounted subtotal — the amount the customer
	// actually pays for goods — then shipping is added (Phase 13).
	taxCents := (subtotal - goodsDiscount) * taxRatePercent / 100

	// Apply the applied gift card (Phase 18): re-validated inside this
	// transaction and claimed via FOR UPDATE, so an expired / disabled /
	// exhausted / deleted card is rejected at checkout even when it applied to
	// the cart earlier, and two concurrent checkouts spending the last dollar
	// of a card can never double-spend it. Claims at most what the order owes
	// (min(balance, amountDue)); unused balance stays on the card and the
	// total never goes below zero.
	shippingNet := quote.CostCents - shippingDiscountCents
	amountDue := subtotal - goodsDiscount + shippingNet + taxCents
	giftCardCents := 0
	if giftCardCode != "" {
		q, err := giftcards.Claim(ctx, tx, tid, giftCardCode, amountDue)
		if err != nil {
			var ce *giftcards.CodeError
			if errors.As(err, &ce) {
				return httperr.C(ce.Status, ce.Message)
			}
			return httperr.ErrInternalServerError
		}
		giftCardCents = q.Cents
	}
	total := amountDue - giftCardCents

	var orderID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO orders (tenant_id, customer_id, customer_name, customer_phone, customer_email,
			shipping_address_line1, shipping_address_line2, shipping_city, shipping_state,
			shipping_postal_code, shipping_country, shipping_method, shipping_cost_cents,
			discount_code, discount_cents, gift_card_code, gift_card_cents, tax_cents,
			payment_method, payment_status, status, total_cents, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, 'pending', $21, $22)
		RETURNING id`,
		tid, customerID, req.CustomerName, req.CustomerPhone, nullableStr(req.CustomerEmail),
		nullableStr(req.ShippingAddressLine1), nullableStr(req.ShippingAddressLine2),
		nullableStr(req.ShippingCity), nullableStr(req.ShippingState),
		nullableStr(req.ShippingPostalCode), nullableStr(req.ShippingCountry),
		nullableStr(quote.Method), quote.CostCents,
		nullableStr(discountCode), discountCents, nullableStr(giftCardCode), giftCardCents, taxCents,
		req.PaymentMethod, res.PaymentStatus, total, currency).Scan(&orderID); err != nil {
		return httperr.ErrInternalServerError
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (tenant_id, order_id, product_id, variant_id, quantity, unit_price_cents, is_preorder, bundle_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			tid, orderID, l.productID, l.variantID, l.quantity, l.price, l.preorder, l.bundleID); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	for _, l := range lines {
		if l.preorder {
			continue // Phase 19 — preorder lines sell before stock exists.
		}
		if _, err := tx.Exec(ctx,
			"UPDATE product_variants SET inventory_count = inventory_count - $1 WHERE id = $2",
			l.quantity, l.variantID); err != nil {
			return httperr.ErrInternalServerError
		}
	}
	// Refresh the cached products.price_cents / products.inventory_count
	// aggregates after the variant decrements, and drop the Redis product
	// detail cache so the public storefront repopulates with fresh stock.
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
		cache.InvalidateProduct(ctx, s.cache, tid, l.productID)
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM cart_items WHERE cart_id = $1", cartID); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM carts WHERE id = $1", cartID); err != nil {
		return httperr.ErrInternalServerError
	}

	auth.AfterCommit(c, func() {
		s.bus.Emit(context.Background(), events.Event{
			Name: "order.created",
			Data: fiber.Map{"order_id": orderID, "tenant_id": tid},
		})
	})

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

// distinctBundleIDs returns the unique non-nil bundle ids referenced by the
// cart's lines, in first-seen order.
func distinctBundleIDs(lines []line) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, l := range lines {
		if l.bundleID != nil {
			if _, ok := seen[*l.bundleID]; !ok {
				seen[*l.bundleID] = struct{}{}
				out = append(out, *l.bundleID)
			}
		}
	}
	return out
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
			&o.giftCardCode, &o.giftCardCents,
			&o.taxCents, &o.internalNote,
			&o.paymentMethod, &o.paymentStatus, &o.status, &o.source, &o.totalCents, &o.currency,
			&o.trackingNumber, &o.trackingCarrier, &o.trackingURL, &o.createdAt); err != nil {
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
			SELECT id, product_id, variant_id, quantity, unit_price_cents, bundle_id
			FROM order_items WHERE order_id = $1 ORDER BY id`, found[i].id)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		for irows.Next() {
			var it orderItemRow
			if err := irows.Scan(&it.id, &it.productID, &it.variantID, &it.quantity, &it.unitPriceCents, &it.bundleID); err != nil {
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
	Status          string  `json:"status"`
	TrackingNumber  *string `json:"tracking_number"` // Phase 23: set when advancing to shipped
	TrackingCarrier *string `json:"tracking_carrier"`
	TrackingURL     *string `json:"tracking_url"`
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

	// Phase 23: tracking fields ride the status update (the shipped transition
	// is the canonical use) — set only when the body provides them, so an
	// absent field is preserved and "" clears it. Parameterized to keep the
	// values out of the concatenated SQL.
	var params []any
	if req.TrackingNumber != nil {
		params = append(params, *req.TrackingNumber)
		sets += fmt.Sprintf(", tracking_number = $%d", len(params))
	}
	if req.TrackingCarrier != nil {
		params = append(params, *req.TrackingCarrier)
		sets += fmt.Sprintf(", tracking_carrier = $%d", len(params))
	}
	if req.TrackingURL != nil {
		params = append(params, *req.TrackingURL)
		sets += fmt.Sprintf(", tracking_url = $%d", len(params))
	}
	params = append(params, c.Params("id"))
	sets += fmt.Sprintf(" WHERE id = $%d", len(params))

	if _, err := tx.Exec(ctx, "UPDATE orders SET "+sets, params...); err != nil {
		return httperr.ErrInternalServerError
	}

	if target == "delivered" {
		tid, _ := c.Locals("tenant_id").(string)
		orderID := strings.Clone(c.Params("id"))
		auth.AfterCommit(c, func() {
			s.bus.Emit(context.Background(), events.Event{
				Name: "order.paid",
				Data: fiber.Map{"order_id": orderID, "tenant_id": tid},
			})
		})
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

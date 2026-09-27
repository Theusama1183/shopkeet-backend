package orders

import (
	"context"
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/cache"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/shipping"
)

// --- draft orders (Phase 15) --------------------------------------------------

// draftLineRequest is one line of a draft order. unit_price_cents is optional:
// when 0 the live variant price is snapshot instead of a merchant override.
type draftLineRequest struct {
	VariantID      string `json:"variant_id"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int    `json:"unit_price_cents"`
}

// draftRequest mirrors the checkout body (same shipping + customer shape) plus
// an optional existing-customer attach and explicit lines with price overrides.
type draftRequest struct {
	CustomerID           string             `json:"customer_id"`
	CustomerName         string             `json:"customer_name"`
	CustomerPhone        string             `json:"customer_phone"`
	CustomerEmail        string             `json:"customer_email"`
	ShippingAddressLine1 string             `json:"shipping_address_line1"`
	ShippingAddressLine2 string             `json:"shipping_address_line2"`
	ShippingCity         string             `json:"shipping_city"`
	ShippingState        string             `json:"shipping_state"`
	ShippingPostalCode   string             `json:"shipping_postal_code"`
	ShippingCountry      string             `json:"shipping_country"`
	ShippingRateID       string             `json:"shipping_rate_id"`
	PaymentMethod        string             `json:"payment_method"`
	Lines                []draftLineRequest `json:"lines"`
}

type draftDevLine struct {
	variantID string
	productID string
	quantity  int
	price     int
}

// CreateDraftOrder handles POST /orders/draft (Admin). A merchant records a
// phone/WhatsApp sale the same way checkout builds a storefront order — same
// FOR UPDATE stock guard, same shipping-rate/tax totals, same inventory
// decrement — but the lines come from the request body (optional per-line price
// override), an existing customer can be attached or left NULL, and the order
// is marked source='draft'. Draft orders are real orders: they consume stock
// exactly like a storefront order.
func (s *Service) CreateDraftOrder(c *fiber.Ctx) error {
	var req draftRequest
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
	if len(req.Lines) == 0 {
		return httperr.C(fiber.StatusBadRequest, "lines required")
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

	var customerID *string
	if req.CustomerID != "" {
		var one int
		if err := tx.QueryRow(ctx, "SELECT 1 FROM customers WHERE id = $1", req.CustomerID).
			Scan(&one); errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "customer not found")
		} else if err != nil {
			return httperr.ErrInternalServerError
		}
		customerID = &req.CustomerID
	}

	// Lock every variant (plus its product row), like checkout. This is the
	// anti-oversell guard: two drafts for the last unit serialize here.
	seen := map[string]bool{}
	var lines []draftDevLine
	currency := ""
	for _, ln := range req.Lines {
		if ln.VariantID == "" || ln.Quantity <= 0 {
			return httperr.C(fiber.StatusBadRequest, "variant_id and quantity > 0 required per line")
		}
		if seen[ln.VariantID] {
			return httperr.C(fiber.StatusBadRequest, "duplicate variant_id in lines")
		}
		seen[ln.VariantID] = true

		var vPrice, vInv int
		var vStatus, pStatus, lineCurr string
		var pID string
		if err := tx.QueryRow(ctx, `
			SELECT v.price_cents, v.inventory_count, v.status, p.id, p.currency, p.status
			FROM product_variants v
			JOIN products p ON p.id = v.product_id
			WHERE v.id = $1
			FOR UPDATE OF v, p`, ln.VariantID).Scan(
			&vPrice, &vInv, &vStatus, &pID, &lineCurr, &pStatus); errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "variant not found")
		} else if err != nil {
			return httperr.ErrInternalServerError
		}
		if vStatus != "active" || pStatus != "active" {
			return httperr.C(fiber.StatusConflict, "a product in the order is no longer available")
		}
		if vInv < ln.Quantity {
			return httperr.C(fiber.StatusConflict, "insufficient stock")
		}
		if currency == "" {
			currency = lineCurr
		}
		price := ln.UnitPriceCents
		if price <= 0 {
			price = vPrice
		}
		lines = append(lines, draftDevLine{variantID: ln.VariantID, productID: pID,
			quantity: ln.Quantity, price: price})
	}

	res, err := provider.Process(ctx, payments.ProcessRequest{
		Method: req.PaymentMethod, AmountCents: 0, Currency: "usd"})
	if err != nil {
		return httperr.ErrInternalServerError
	}

	subtotal := 0
	for _, l := range lines {
		subtotal += l.quantity * l.price
	}

	var taxRatePercent int
	if err := tx.QueryRow(ctx, `SELECT tax_rate_percent FROM tenants WHERE id = $1`, tid).
		Scan(&taxRatePercent); err != nil {
		taxRatePercent = 0
	}

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

	taxCents := subtotal * taxRatePercent / 100
	total := subtotal + quote.CostCents + taxCents

	var orderID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO orders (tenant_id, customer_id, customer_name, customer_phone, customer_email,
			shipping_address_line1, shipping_address_line2, shipping_city, shipping_state,
			shipping_postal_code, shipping_country, shipping_method, shipping_cost_cents,
			discount_code, discount_cents, tax_cents,
			payment_method, payment_status, status, source, total_cents, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, 'pending', 'draft', $19, $20)
		RETURNING id`,
		tid, customerID, req.CustomerName, req.CustomerPhone, nullableStr(req.CustomerEmail),
		nullableStr(req.ShippingAddressLine1), nullableStr(req.ShippingAddressLine2),
		nullableStr(req.ShippingCity), nullableStr(req.ShippingState),
		nullableStr(req.ShippingPostalCode), nullableStr(req.ShippingCountry),
		nullableStr(quote.Method), quote.CostCents,
		nil, 0, taxCents,
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
	// Same aggregate refresh + cache invalidation as checkout: a draft consumes
	// real stock, so the storefront must repopulate with fresh numbers.
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

	auth.AfterCommit(c, func() {
		s.bus.Emit(context.Background(), events.Event{
			Name: "order.created",
			Data: fiber.Map{"order_id": orderID, "tenant_id": tid, "source": "draft"},
		})
	})

	order, err := loadOrder(c, tx, "id = $1", orderID)
	if err != nil || order == nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(orderJSON(order, true))
}

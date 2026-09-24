package orders

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
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
	quantity       int
	unitPriceCents int
}

type orderRow struct {
	id              string
	customerName    string
	customerPhone   string
	customerEmail   *string
	shippingAddress string
	paymentMethod   string
	paymentStatus   string
	status          string
	totalCents      int
	currency        string
	createdAt       time.Time
	items           []orderItemRow
}

const orderSelect = `
	SELECT id, customer_name, customer_phone, customer_email, shipping_address,
	       payment_method, payment_status, status, total_cents, currency, created_at
	FROM orders`

// loadOrder hydrates one order plus its items.
func loadOrder(c *fiber.Ctx, tx pgx.Tx, where string, args ...any) (*orderRow, error) {
	ctx := c.Context()
	var o orderRow
	err := tx.QueryRow(ctx, orderSelect+" WHERE "+where, args...).
		Scan(&o.id, &o.customerName, &o.customerPhone, &o.customerEmail, &o.shippingAddress,
			&o.paymentMethod, &o.paymentStatus, &o.status, &o.totalCents, &o.currency, &o.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT id, product_id, quantity, unit_price_cents
		FROM order_items
		WHERE order_id = $1
		ORDER BY id`, o.id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var it orderItemRow
		if err := rows.Scan(&it.id, &it.productID, &it.quantity, &it.unitPriceCents); err != nil {
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

func orderJSON(o *orderRow) fiber.Map {
	items := make([]fiber.Map, 0, len(o.items))
	for _, it := range o.items {
		items = append(items, fiber.Map{
			"id": it.id, "product_id": it.productID, "quantity": it.quantity,
			"unit_price_cents": it.unitPriceCents,
			"line_total_cents": it.quantity * it.unitPriceCents,
		})
	}
	return fiber.Map{
		"id": o.id, "customer_name": o.customerName, "customer_phone": o.customerPhone,
		"customer_email": strp(o.customerEmail), "shipping_address": o.shippingAddress,
		"payment_method": o.paymentMethod, "payment_status": o.paymentStatus,
		"status": o.status, "total_cents": o.totalCents, "currency": o.currency,
		"created_at": o.createdAt.Format(time.RFC3339), "items": items,
	}
}

// --- checkout -----------------------------------------------------------------

type checkoutRequest struct {
	CustomerName    string `json:"customer_name"`
	CustomerPhone   string `json:"customer_phone"`
	CustomerEmail   string `json:"customer_email"`
	ShippingAddress string `json:"shipping_address"`
	PaymentMethod   string `json:"payment_method"`
}

// Checkout handles POST /checkout (Customer). Runs inside the request
// transaction: locks product rows FOR UPDATE (the authoritative anti-oversell
// guard), rejects lines that can't be fulfilled, snapshots unit prices, creates
// the order, decrements inventory, clears the cart, and emits order.created.
func (s *Service) Checkout(c *fiber.Ctx) error {
	var req checkoutRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.CustomerName == "" || req.CustomerPhone == "" || req.ShippingAddress == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "customer_name, customer_phone, shipping_address required"})
	}
	if req.PaymentMethod == "" {
		req.PaymentMethod = "cod"
	}
	provider, ok := s.payments.Get(req.PaymentMethod)
	if !ok {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unsupported payment method"})
	}

	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	var cartID string
	if err := tx.QueryRow(ctx,
		"SELECT id FROM carts WHERE customer_session = $1", session).Scan(&cartID); errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cart is empty"})
	}

	// Lock every product in the cart. This is the real guard against
	// overselling: two concurrent checkouts for the last unit serialize here,
	// and the second sees the already-decremented inventory.
	rows, err := tx.Query(ctx, `
		SELECT ci.product_id, ci.quantity, p.price_cents, p.currency,
		       p.inventory_count, p.status
		FROM cart_items ci
		JOIN products p ON p.id = ci.product_id
		WHERE ci.cart_id = $1
		ORDER BY p.name
		FOR UPDATE OF p`, cartID)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	type line struct {
		productID string
		quantity  int
		price     int
		currency  string
	}
	var lines []line
	for rows.Next() {
		var l line
		var inventory int
		var status string
		if err := rows.Scan(&l.productID, &l.quantity, &l.price, &l.currency, &inventory, &status); err != nil {
			rows.Close()
			return fiber.ErrInternalServerError
		}
		if status != "active" {
			rows.Close()
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "a product in your cart is no longer available"})
		}
		if inventory < l.quantity {
			rows.Close()
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "insufficient stock"})
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return fiber.ErrInternalServerError
	}
	rows.Close()
	if len(lines) == 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "cart is empty"})
	}

	res, err := provider.Process(ctx, payments.ProcessRequest{
		Method: req.PaymentMethod, AmountCents: 0, Currency: "usd"})
	if err != nil {
		return fiber.ErrInternalServerError
	}

	total := 0
	currency := lines[0].currency
	for _, l := range lines {
		total += l.quantity * l.price
	}

	var orderID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO orders (tenant_id, customer_name, customer_phone, customer_email,
			shipping_address, payment_method, payment_status, status, total_cents, currency)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'pending', $8, $9)
		RETURNING id`,
		tid, req.CustomerName, req.CustomerPhone, nullableStr(req.CustomerEmail),
		req.ShippingAddress, req.PaymentMethod, res.PaymentStatus, total, currency).Scan(&orderID); err != nil {
		return fiber.ErrInternalServerError
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx, `
			INSERT INTO order_items (tenant_id, order_id, product_id, quantity, unit_price_cents)
			VALUES ($1, $2, $3, $4, $5)`, tid, orderID, l.productID, l.quantity, l.price); err != nil {
			return fiber.ErrInternalServerError
		}
	}
	for _, l := range lines {
		if _, err := tx.Exec(ctx,
			"UPDATE products SET inventory_count = inventory_count - $1 WHERE id = $2",
			l.quantity, l.productID); err != nil {
			return fiber.ErrInternalServerError
		}
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM cart_items WHERE cart_id = $1", cartID); err != nil {
		return fiber.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM carts WHERE id = $1", cartID); err != nil {
		return fiber.ErrInternalServerError
	}

	if err := s.bus.Emit(ctx, events.Event{Name: "order.created", Data: fiber.Map{"order_id": orderID}}); err != nil {
		return fiber.ErrInternalServerError
	}

	order, err := loadOrder(c, tx, "id = $1", orderID)
	if err != nil || order == nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(orderJSON(order))
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "phone query param required"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
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
		return fiber.ErrInternalServerError
	}
	if order == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "order not found"})
	}
	return c.JSON(orderJSON(order))
}

// --- admin: list & status ------------------------------------------------------

// ListOrders handles GET /orders (Admin). Returns the tenant's orders, newest
// first, optionally filtered by ?status=, each with its items.
func (s *Service) ListOrders(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
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
		return fiber.ErrInternalServerError
	}
	defer rows.Close()

	var found []orderRow
	for rows.Next() {
		var o orderRow
		if err := rows.Scan(&o.id, &o.customerName, &o.customerPhone, &o.customerEmail, &o.shippingAddress,
			&o.paymentMethod, &o.paymentStatus, &o.status, &o.totalCents, &o.currency, &o.createdAt); err != nil {
			return fiber.ErrInternalServerError
		}
		found = append(found, o)
	}
	if err := rows.Err(); err != nil {
		return fiber.ErrInternalServerError
	}
	rows.Close()

	// Hydrate items after the cursor is closed (pgx refuses a second query on
	// an open connection otherwise).
	ordersJSON := make([]fiber.Map, 0, len(found))
	for i := range found {
		var items []orderItemRow
		irows, err := tx.Query(ctx, `
			SELECT id, product_id, quantity, unit_price_cents
			FROM order_items WHERE order_id = $1 ORDER BY id`, found[i].id)
		if err != nil {
			return fiber.ErrInternalServerError
		}
		for irows.Next() {
			var it orderItemRow
			if err := irows.Scan(&it.id, &it.productID, &it.quantity, &it.unitPriceCents); err != nil {
				irows.Close()
				return fiber.ErrInternalServerError
			}
			items = append(items, it)
		}
		if err := irows.Err(); err != nil {
			return fiber.ErrInternalServerError
		}
		irows.Close()
		found[i].items = items
		ordersJSON = append(ordersJSON, orderJSON(&found[i]))
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()

	var current string
	if err := tx.QueryRow(ctx,
		"SELECT status FROM orders WHERE id = $1 FOR UPDATE", c.Params("id")).Scan(&current); errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "order not found"})
	}

	target := req.Status
	var sets string
	switch target {
	case "cancelled":
		if current != "pending" && current != "confirmed" {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status transition"})
		}
		sets = "status = 'cancelled'"
	case "confirmed", "shipped", "delivered":
		if nextStatus[current] != target {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status transition"})
		}
		sets = "status = '" + target + "'"
		if target == "delivered" {
			sets += ", payment_status = 'paid'"
		}
	default:
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}

	if _, err := tx.Exec(ctx,
		"UPDATE orders SET "+sets+" WHERE id = $1", c.Params("id")); err != nil {
		return fiber.ErrInternalServerError
	}

	if target == "delivered" {
		if err := s.bus.Emit(ctx, events.Event{Name: "order.paid", Data: fiber.Map{"order_id": c.Params("id")}}); err != nil {
			return fiber.ErrInternalServerError
		}
	}

	order, err := loadOrder(c, tx, "id = $1", c.Params("id"))
	if err != nil || order == nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(orderJSON(order))
}

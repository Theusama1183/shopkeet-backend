package cart

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	name           string
	slug           string
	quantity       int
	priceCents     int
	currency       string
	lineTotalCents int
}

type cartPayload struct {
	id       string
	items    []itemRow
	total    int
	currency string
}

// loadCart returns the customer's cart with its items (product info joined in)
// and totals. Returns (nil, nil) when the session has no cart yet.
func loadCart(c *fiber.Ctx, tx pgx.Tx, session string) (*cartPayload, error) {
	ctx := c.Context()
	var cartID string
	err := tx.QueryRow(ctx,
		"SELECT id FROM carts WHERE customer_session = $1", session).Scan(&cartID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	rows, err := tx.Query(ctx, `
		SELECT ci.id, ci.product_id, ci.quantity,
		       p.name, p.slug, p.price_cents, p.currency
		FROM cart_items ci
		JOIN products p ON p.id = ci.product_id
		WHERE ci.cart_id = $1
		ORDER BY p.name`, cartID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	cp := &cartPayload{id: cartID}
	for rows.Next() {
		var it itemRow
		if err := rows.Scan(&it.id, &it.productID, &it.quantity,
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
			"name":             it.name,
			"slug":             it.slug,
			"quantity":         it.quantity,
			"price_cents":      it.priceCents,
			"currency":         it.currency,
			"line_total_cents": it.lineTotalCents,
		})
	}
	return fiber.Map{"cart": fiber.Map{
		"id":          cp.id,
		"items":       items,
		"total_cents": cp.total,
		"currency":    cp.currency,
	}}
}

// --- handlers -------------------------------------------------------------------

// GetCart handles GET /cart (Customer). Returns {"cart": null} when this
// session has no cart yet — a fresh guest always has a valid empty cart.
func (s *Service) GetCart(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

type addItemRequest struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

// AddItem handles POST /cart (Customer). Creates the cart on first use and
// merges quantity for a product already in the cart. Only active products can
// be carted. Stock is deliberately NOT gated here — checkout arbitrates
// (docs/04-agent-build-spec.md Phase 4 acceptance). Best-effort Redis reserve
// on the way out.
func (s *Service) AddItem(c *fiber.Ctx) error {
	var req addItemRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.ProductID == "" || req.Quantity < 1 || req.Quantity > 999 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "product_id and quantity (1-999) required"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	// Product must exist and be active (RLS-scoped by the request tenant).
	var active bool
	err := tx.QueryRow(ctx,
		"SELECT (status = 'active') FROM products WHERE id = $1", req.ProductID).Scan(&active)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !active) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "product not found"})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}

	// One cart per session (UNIQUE (tenant_id, customer_session)); merge lines.
	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return fiber.ErrInternalServerError
	}
	var cartID string
	if err := tx.QueryRow(ctx,
		"SELECT id FROM carts WHERE customer_session = $1", session).Scan(&cartID); err != nil {
		return fiber.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO cart_items (tenant_id, cart_id, product_id, quantity)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (cart_id, product_id)
		DO UPDATE SET quantity = cart_items.quantity + EXCLUDED.quantity`,
		tid, cartID, req.ProductID, req.Quantity); err != nil {
		return fiber.ErrInternalServerError
	}

	// Fast-path reservation; never fails the request (checkout is authoritative).
	_ = s.reserver.ReserveUnits(ctx, req.ProductID, req.Quantity)

	cp, err := loadCart(c, tx, session)
	if err != nil {
		return fiber.ErrInternalServerError
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
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.Quantity < 1 || req.Quantity > 999 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "quantity (1-999) required"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), `
		UPDATE cart_items ci SET quantity = $1
		FROM carts c
		WHERE ci.id = $2 AND ci.cart_id = c.id AND c.customer_session = $3`,
		req.Quantity, c.Params("id"), customerSession(c))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "cart item not found"})
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

// RemoveItem handles DELETE /cart/items/:id (Customer).
func (s *Service) RemoveItem(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), `
		DELETE FROM cart_items ci
		USING carts c
		WHERE ci.id = $1 AND ci.cart_id = c.id AND c.customer_session = $2`,
		c.Params("id"), customerSession(c))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "cart item not found"})
	}
	cp, err := loadCart(c, tx, customerSession(c))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

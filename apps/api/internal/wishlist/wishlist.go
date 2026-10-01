// Package wishlist implements Phase 22 — customer wishlists (Wishlist Plus
// replacement). A wishlist is scoped per tenant+customer; the UNIQUE constraint
// on (customer_id, product_id) turns a duplicate add into a 23505, answered 409.
// Every route runs behind CustomerAuthMW, which opens the request transaction,
// SET LOCALs app.current_tenant for RLS, and sets c.Locals("customer_id").
package wishlist

import (
	"errors"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service serves the wishlist surface under /customers/me/wishlist.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// --- helpers (same contract as customers/reviews) ----------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok
}

func customerID(c *fiber.Ctx) string {
	if v, ok := c.Locals("customer_id").(string); ok {
		return v
	}
	return ""
}

func tenantID(c *fiber.Ctx) string {
	if v, ok := c.Locals("tenant_id").(string); ok {
		return v
	}
	return ""
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// --- queries -----------------------------------------------------------------

const itemSelect = `
	SELECT w.id, w.product_id, p.name, p.slug, p.price_cents, p.currency,
	       p.status, w.created_at
	FROM wishlist_items w
	JOIN products p ON p.id = w.product_id`

type itemRow struct {
	id        string
	productID string
	name      string
	slug      string
	price     int
	currency  string
	status    string
	createdAt time.Time
}

func itemJSON(r itemRow) fiber.Map {
	return fiber.Map{
		"id":           r.id,
		"product_id":   r.productID,
		"product_name": r.name,
		"product_slug": r.slug,
		"price_cents":  r.price,
		"currency":     r.currency,
		"status":       r.status,
		"created_at":   r.createdAt.Format(time.RFC3339),
	}
}

// --- handlers ----------------------------------------------------------------

// ListWishlist handles GET /customers/me/wishlist (Customer). Returns the
// customer's saved products, newest first, joined to live product data so the
// storefront can render names, prices, and availability in one round trip.
func (s *Service) ListWishlist(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	cid := customerID(c)

	rows, err := tx.Query(ctx, itemSelect+`
		WHERE w.customer_id = $1
		ORDER BY w.created_at DESC, w.id`, cid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	list := make([]fiber.Map, 0)
	for rows.Next() {
		var it itemRow
		if err := rows.Scan(&it.id, &it.productID, &it.name, &it.slug,
			&it.price, &it.currency, &it.status, &it.createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		list = append(list, itemJSON(it))
	}
	return c.JSON(fiber.Map{"items": list})
}

// AddWishlistItem handles POST /customers/me/wishlist and
// POST /customers/me/wishlist/:productId (Customer). The product comes from the
// body's product_id or the route param. A duplicate add answers 409; unknown
// product answers 404.
func (s *Service) AddWishlistItem(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	cid := customerID(c)

	productID := c.Params("productId")
	if productID == "" {
		var req struct {
			ProductID string `json:"product_id"`
		}
		if err := c.BodyParser(&req); err != nil || req.ProductID == "" {
			return httperr.C(fiber.StatusBadRequest, "product_id required")
		}
		productID = req.ProductID
	}

	var exists int
	if err := tx.QueryRow(ctx,
		"SELECT 1 FROM products WHERE id = $1", productID).Scan(&exists); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return httperr.C(fiber.StatusNotFound, "product not found")
		}
		return httperr.ErrInternalServerError
	}

	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO wishlist_items (tenant_id, customer_id, product_id)
		VALUES ($1, $2, $3)
		RETURNING id`, tenantID(c), cid, productID).Scan(&id)
	if err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "already in wishlist")
		}
		return httperr.ErrInternalServerError
	}

	var it itemRow
	if err := tx.QueryRow(ctx, itemSelect+
		" WHERE w.id = $1", id).Scan(&it.id, &it.productID, &it.name, &it.slug,
		&it.price, &it.currency, &it.status, &it.createdAt); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(itemJSON(it))
}

// RemoveWishlistItem handles DELETE /customers/me/wishlist/:productId
// (Customer). Removing an item that isn't on the list answers 404 (idempotent
// delete would mask typos and is not what the storefront needs).
func (s *Service) RemoveWishlistItem(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	cid := customerID(c)

	tag, err := tx.Exec(c.Context(), `
		DELETE FROM wishlist_items
		WHERE customer_id = $1 AND product_id = $2`,
		cid, c.Params("productId"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "not in wishlist")
	}
	return c.SendStatus(fiber.StatusNoContent)
}

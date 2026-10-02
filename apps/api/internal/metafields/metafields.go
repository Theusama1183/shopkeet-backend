// Package metafields implements Phase 28 — Custom Fields (metafields).
// Merchants attach arbitrary key/value pairs (material, care instructions,
// spec sheets, …) to a product without any schema change or deploy; the next
// key they invent just works. The public GET /products/:id response embeds
// them; admin write routes live under /products/:id/metafields.
package metafields

import (
	"context"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/cache"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service handles the product metafield surface. Handlers read/write through
// the request transaction the auth middleware opened (c.Locals("tx")), so RLS
// scopes every query to the resolved tenant.
type Service struct {
	pool  *pgxpool.Pool
	cache cache.Cache
}

// New builds a metafields Service with caching disabled until SetCache.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool, cache: cache.Noop{}}
}

// SetCache enables catalog cache invalidation so a metafield write never
// leaves a stale public product detail cached.
func (s *Service) SetCache(c cache.Cache) {
	if c != nil {
		s.cache = c
	}
}

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

func productExists(ctx context.Context, tx pgx.Tx, productID string) bool {
	// A non-UUID id ("does-not-exist") can't reference a product — treat it as
	// not-found rather than letting Postgres 22P02 surface as a 500.
	if _, err := uuid.Parse(productID); err != nil {
		return false
	}
	return tx.QueryRow(ctx,
		"SELECT true FROM products WHERE id = $1", productID).Scan(new(bool)) == nil
}

func (s *Service) invalidateProduct(ctx context.Context, tid, productID string) {
	cache.InvalidateProduct(ctx, s.cache, tid, productID)
}

// --- handlers -------------------------------------------------------------------

// List handles GET /products/:id/metafields (admin). 404s when the product
// doesn't exist in this tenant; otherwise returns the metafields (possibly
// empty) as {product_id, metafields: [{key, value, type}]}.
func (s *Service) List(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	productID := c.Params("id")

	if !productExists(ctx, tx, productID) {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}

	rows, err := tx.Query(ctx, `
		SELECT key, value, type FROM product_metafields
		WHERE product_id = $1
		ORDER BY key`, productID)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	fields := make([]fiber.Map, 0)
	for rows.Next() {
		var key, value, typ string
		if err := rows.Scan(&key, &value, &typ); err != nil {
			return httperr.ErrInternalServerError
		}
		fields = append(fields, fiber.Map{"key": key, "value": value, "type": typ})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"product_id": productID, "metafields": fields})
}

// Upsert handles PUT /products/:id/metafields/:key (admin). Creates the
// metafield or updates it in place (idempotent). Invalidates the cached
// public product detail either way.
func (s *Service) Upsert(c *fiber.Ctx) error {
	var req struct {
		Value string `json:"value"`
		Type  string `json:"type"`
	}
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	req.Value = strings.TrimSpace(req.Value)
	if req.Value == "" {
		return httperr.C(fiber.StatusBadRequest, "value required")
	}
	key := strings.TrimSpace(c.Params("key"))
	if key == "" {
		return httperr.C(fiber.StatusBadRequest, "key required")
	}
	if req.Type == "" {
		req.Type = "text"
	}
	if !validType(req.Type) {
		return httperr.C(fiber.StatusBadRequest, "invalid type (text|number|boolean|json)")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	productID := c.Params("id")
	tid := tenantID(c)

	if !productExists(ctx, tx, productID) {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO product_metafields (tenant_id, product_id, key, value, type)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (product_id, key)
		DO UPDATE SET value = EXCLUDED.value, type = EXCLUDED.type, updated_at = now()`,
		tid, productID, key, req.Value, req.Type); err != nil {
		return httperr.ErrInternalServerError
	}

	s.invalidateProduct(ctx, tid, productID)
	return c.JSON(fiber.Map{"key": key, "value": req.Value, "type": req.Type})
}

// Delete handles DELETE /products/:id/metafields/:key (admin). 404 when the
// key doesn't exist for the product.
func (s *Service) Delete(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	productID := c.Params("id")
	key := strings.TrimSpace(c.Params("key"))
	tid := tenantID(c)

	ct, err := tx.Exec(ctx,
		"DELETE FROM product_metafields WHERE product_id = $1 AND key = $2",
		productID, key)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if ct.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "metafield not found")
	}

	s.invalidateProduct(ctx, tid, productID)
	return c.JSON(fiber.Map{"deleted": true})
}

// --- helpers --------------------------------------------------------------------

func validType(t string) bool {
	switch t {
	case "text", "number", "boolean", "json":
		return true
	}
	return false
}
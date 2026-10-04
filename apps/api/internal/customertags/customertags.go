// Package customertags implements Phase 33: customer tags & segments. Merchants
// tag customers (vip, wholesale, newsletter, …) and can list customers filtered
// by a tag; discount codes carry an optional eligible_tag that checkout enforces
// (see internal/discounts). Tags are tenant-scoped by the same RLS contract as
// every other tenant table, so an admin's tags are invisible to other tenants.
package customertags

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// maxTagLen caps a tag's canonical length; enough for segments like "bulk-order".
const maxTagLen = 64

type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

// normalizeTag canonicalizes a tag for storage/comparison: trimmed, lowercased,
// no spaces, reasonably short. Both the tag endpoints and the discount
// eligible_tag setter go through this shape so a merchant's "VIP" discount gate
// reliably matches a "vip" customer tag.
func normalizeTag(raw string) (string, error) {
	tag := strings.ToLower(strings.TrimSpace(raw))
	if tag == "" {
		return "", httperr.C(fiber.StatusBadRequest, "tag required")
	}
	if len(tag) > maxTagLen {
		return "", httperr.C(fiber.StatusBadRequest, "tag is too long (max 64 chars)")
	}
	if strings.ContainsAny(tag, " \t") {
		return "", httperr.C(fiber.StatusBadRequest, "tag must not contain spaces")
	}
	return tag, nil
}

// customerExists scopes the ":id" in every tag endpoint: RLS (this request
// transaction) makes a cross-tenant id invisible, hence "not found".
func customerExists(ctx *fiber.Ctx, tx pgx.Tx, customerID string) (bool, error) {
	var exists bool
	if err := tx.QueryRow(ctx.Context(),
		"SELECT EXISTS (SELECT 1 FROM customers WHERE id = $1)", customerID).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}

type tagRequest struct {
	Tag string `json:"tag"`
}

// AddTag handles POST /customers/:id/tags (Admin). Idempotent: tagging a
// customer again is a no-op success.
func (s *Service) AddTag(c *fiber.Ctx) error {
	var req tagRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tag, err := normalizeTag(req.Tag)
	if err != nil {
		return err
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	cid := c.Params("id")
	exists, err := customerExists(c, tx, cid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if !exists {
		return httperr.C(fiber.StatusNotFound, "customer not found")
	}
	if _, err := tx.Exec(c.Context(), `
		INSERT INTO customer_tags (tenant_id, customer_id, tag)
		VALUES ($1, $2, $3)
		ON CONFLICT (tenant_id, customer_id, tag) DO NOTHING`, tenantID(c), cid, tag); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{"customer_id": cid, "tag": tag})
}

// RemoveTag handles DELETE /customers/:id/tags?tag=vip (Admin). Returns 404 when
// the customer exists but carries no such tag.
func (s *Service) RemoveTag(c *fiber.Ctx) error {
	tag, err := normalizeTag(c.Query("tag"))
	if err != nil {
		return err
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	cid := c.Params("id")
	exists, err := customerExists(c, tx, cid)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if !exists {
		return httperr.C(fiber.StatusNotFound, "customer not found")
	}
	tagResult, err := tx.Exec(c.Context(),
		"DELETE FROM customer_tags WHERE customer_id = $1 AND tag = $2", cid, tag)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tagResult.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "customer is not tagged "+tag)
	}
	return c.JSON(fiber.Map{"customer_id": cid, "tag": tag, "removed": 1})
}

type customerOut struct {
	ID    string   `json:"id"`
	Email string   `json:"email"`
	Phone string   `json:"phone"`
	Tags  []string `json:"tags"`
}

// ListCustomers handles GET /customers (Admin). Optional ?tag=vip narrows the
// segment to customers carrying that tag; absent, it lists every customer with
// their full tag set. This is the Phase 33 "segments" surface.
func (s *Service) ListCustomers(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag := strings.TrimSpace(c.Query("tag"))
	rows, err := tx.Query(c.Context(), `
		SELECT c.id,
		       COALESCE(c.email, ''),
		       COALESCE(c.phone, ''),
		       COALESCE(array_agg(ct.tag ORDER BY ct.created_at) FILTER (WHERE ct.tag IS NOT NULL), '{}')
		FROM customers c
		LEFT JOIN customer_tags ct ON ct.tenant_id = c.tenant_id AND ct.customer_id = c.id
		WHERE c.tenant_id = $1
		  AND ($2 = '' OR EXISTS (
		      SELECT 1 FROM customer_tags t WHERE t.customer_id = c.id AND t.tag = $2))
		GROUP BY c.id
		ORDER BY c.created_at DESC, c.id`, tenantID(c), tag)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	out := []customerOut{}
	for rows.Next() {
		var cu customerOut
		if err := rows.Scan(&cu.ID, &cu.Email, &cu.Phone, &cu.Tags); err != nil {
			return httperr.ErrInternalServerError
		}
		if cu.Tags == nil {
			cu.Tags = []string{}
		}
		out = append(out, cu)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"customers": out})
}
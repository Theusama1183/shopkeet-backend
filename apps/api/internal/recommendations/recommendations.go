package recommendations

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service implements the Phase 26 curated recommendations surface: the
// storefront "you may also like" listing plus the admin manual curation
// endpoints. Every query runs inside the RLS-scoped request transaction opened
// by the middleware (c.Locals("tx")), so tenant isolation is guaranteed for
// free. type='auto' rows are reserved for the Phase 32 scheduled job and
// served (and deletable) alongside manual ones.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func isAdmin(c *fiber.Ctx) bool {
	v, _ := c.Locals("admin").(bool)
	return v
}

// activeProduct reports whether the product exists and is sellable. The admin
// create path uses it to reject curation against missing/archived products.
func (s *Service) activeProduct(c *fiber.Ctx, tx pgx.Tx, productID string) (bool, error) {
	var n int
	err := tx.QueryRow(c.Context(),
		"SELECT 1 FROM products WHERE id = $1 AND status = 'active'", productID).Scan(&n)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// productExists reports whether the product exists at all inside the tenant
// (RLS scopes it). Used to 404 clearly when the route's product id is bogus.
func (s *Service) productExists(c *fiber.Ctx, tx pgx.Tx, productID string) (bool, error) {
	var n int
	err := tx.QueryRow(c.Context(),
		"SELECT COUNT(*) FROM products WHERE id = $1", productID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// --- JSON shape ---------------------------------------------------------------

type recommendationRow struct {
	recID      string
	productID  string
	rtype      string
	sortOrder  int
	name       string
	slug       string
	priceCents int
	status     string
}

const recommendationSelect = `
	SELECT r.id, r.recommended_product_id, r.type, r.sort_order,
	       p.name, p.slug, p.price_cents, p.status
	FROM product_recommendations r
	JOIN products p ON p.id = r.recommended_product_id`

func recommendationJSON(r recommendationRow, public bool) fiber.Map {
	m := fiber.Map{
		"id": r.recID, "product_id": r.productID,
		"type": r.rtype, "sort_order": r.sortOrder,
		"recommended_product": fiber.Map{
			"id": r.productID, "name": r.name, "slug": r.slug, "price_cents": r.priceCents,
		},
	}
	if !public {
		m["status"] = r.status
	}
	return m
}

// --- public storefront ----------------------------------------------------------

// ListRecommendations handles GET /products/:id/recommendations. Served to the
// Public storefront (active products only, so a dead link never renders) and
// to Admins (all statuses, so an archived pick is still visible for pruning).
func (s *Service) ListRecommendations(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	ok, err := s.productExists(c, tx, c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if !ok {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}

	public := !isAdmin(c)
	where := "WHERE r.product_id = $1"
	if public {
		where += " AND p.status = 'active'"
	}
	// Phase 32 — manual curation wins: a hand-picked recommendation hides a
	// computed (auto) row for the same pair, and manual rows sort first.
	where += manualPriority
	rows, err := tx.Query(ctx,
		recommendationSelect+" "+where+
			" ORDER BY CASE WHEN r.type = 'manual' THEN 0 ELSE 1 END, r.sort_order, p.name",
		c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	var found []fiber.Map
	for rows.Next() {
		var r recommendationRow
		if err := rows.Scan(&r.recID, &r.productID, &r.rtype, &r.sortOrder,
			&r.name, &r.slug, &r.priceCents, &r.status); err != nil {
			return httperr.ErrInternalServerError
		}
		found = append(found, recommendationJSON(r, public))
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"recommendations": found})
}

// --- admin: manual curation ------------------------------------------------------

type createRecommendationRequest struct {
	RecommendedProductID string `json:"recommended_product_id"`
	Type                 string `json:"type"`
	SortOrder            *int   `json:"sort_order"`
}

// CreateRecommendation handles POST /products/:id/recommendations (Admin).
// Manually curates one recommendation; type defaults to 'manual' (auto is
// reserved for the Phase 32 job's upsert, but accepted so a merchant can
// override a computed pick).
func (s *Service) CreateRecommendation(c *fiber.Ctx) error {
	var req createRecommendationRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	req.RecommendedProductID = strings.TrimSpace(req.RecommendedProductID)
	if req.RecommendedProductID == "" {
		return httperr.C(fiber.StatusBadRequest, "recommended_product_id required")
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if req.Type == "" {
		req.Type = "manual"
	}
	if req.Type != "manual" && req.Type != "auto" {
		return httperr.C(fiber.StatusBadRequest, "type must be 'manual' or 'auto'")
	}
	if req.RecommendedProductID == c.Params("id") {
		return httperr.C(fiber.StatusBadRequest, "a product cannot recommend itself")
	}
	sortOrder := 0
	if req.SortOrder != nil {
		sortOrder = *req.SortOrder
	}
	if sortOrder < 0 {
		return httperr.C(fiber.StatusBadRequest, "sort_order must be >= 0")
	}

	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)

	for _, pid := range []string{c.Params("id"), req.RecommendedProductID} {
		ok, err := s.activeProduct(c, tx, pid)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if !ok {
			return httperr.C(fiber.StatusBadRequest, "product not found or not active")
		}
	}

	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO product_recommendations (tenant_id, product_id, recommended_product_id, type, sort_order)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		tid, c.Params("id"), req.RecommendedProductID, req.Type, sortOrder).Scan(&id)
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) {
			switch pg.Code {
			case "23505":
				return httperr.C(fiber.StatusConflict, "that recommendation already exists")
			case "23503":
				return httperr.C(fiber.StatusBadRequest, "referenced product does not exist")
			}
		}
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id": id, "product_id": c.Params("id"),
		"recommended_product_id": req.RecommendedProductID,
		"type":                   req.Type, "sort_order": sortOrder,
	})
}

// DeleteRecommendation handles DELETE /products/:id/recommendations/:rid
// (Admin). Removes one curated pick; a bogus id 404s.
func (s *Service) DeleteRecommendation(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), `
		DELETE FROM product_recommendations WHERE id = $1 AND product_id = $2`,
		c.Params("rid"), c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "recommendation not found")
	}
	return c.JSON(fiber.Map{"deleted": true})
}

package catalog

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Service is the catalog business surface. Handlers read/write through the
// request transaction TenantMW/PublicTenantMW opened (c.Locals("tx")), so RLS
// scopes every query to the resolved tenant.
type Service struct {
	pool *pgxpool.Pool
}

// New builds a catalog Service.
func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// --- helpers ------------------------------------------------------------------

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

// savepoint runs fn inside a SAVEPOINT so a failing statement (e.g. a unique
// violation that maps to a 409/400) does not poison the whole request
// transaction. Handlers run under TenantMW which auto-commits on success; a
// statement error would otherwise leave the tx in "aborted" state and the
// final Commit would fail (500 "commit unexpectedly resulted in rollback").
func savepoint(ctx context.Context, tx pgx.Tx, fn func() error) error {
	if _, err := tx.Exec(ctx, "SAVEPOINT catalog_op"); err != nil {
		return err
	}
	if err := fn(); err != nil {
		_, _ = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT catalog_op")
		return err
	}
	_, err := tx.Exec(ctx, "RELEASE SAVEPOINT catalog_op")
	return err
}

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func isAdmin(c *fiber.Ctx) bool {
	v, _ := c.Locals("admin").(bool)
	return v
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

// imageRow is a product image joined with its media asset.
type imageRow struct {
	id        string
	url       string
	altText   *string
	sortOrder int
}

type categoryRow struct {
	id   string
	name string
	slug string
}

// productRow mirrors a products row plus its images/categories.
type productRow struct {
	id              string
	name            string
	slug            string
	description     *string
	priceCents      int
	currency        string
	inventoryCount  int
	status          string
	metaTitle       *string
	metaDescription *string
	createdAt       time.Time
	images          []imageRow
	categories      []categoryRow
}

func strp(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func productJSON(p *productRow) fiber.Map {
	images := make([]fiber.Map, 0, len(p.images))
	for _, im := range p.images {
		images = append(images, fiber.Map{
			"id": im.id, "url": im.url, "alt_text": strp(im.altText), "sort_order": im.sortOrder,
		})
	}
	cats := make([]fiber.Map, 0, len(p.categories))
	for _, ct := range p.categories {
		cats = append(cats, fiber.Map{"id": ct.id, "name": ct.name, "slug": ct.slug})
	}
	return fiber.Map{
		"id": p.id, "name": p.name, "slug": p.slug,
		"description": strp(p.description), "price_cents": p.priceCents,
		"currency": p.currency, "inventory_count": p.inventoryCount,
		"status": p.status, "meta_title": strp(p.metaTitle),
		"meta_description": strp(p.metaDescription),
		"created_at":       p.createdAt.Format(time.RFC3339),
		"images":           images,
		"categories":       cats,
	}
}

// queryProduct runs the shared product SELECT and hydrates one row's images and
// categories (in the same query for images via array_agg... simpler: separate
// aggregation queries). Returns (nil,nil) when the product doesn't exist.
func (s *Service) queryProduct(ctx *fiber.Ctx, tx pgx.Tx, where string, args ...any) (*productRow, error) {
	row := tx.QueryRow(ctx.Context(), fmt.Sprintf(`
		SELECT p.id, p.name, p.slug, p.description, p.price_cents, p.currency,
		       p.inventory_count, p.status, p.meta_title, p.meta_description, p.created_at
		FROM products p
		WHERE %s`, where), args...)
	var p productRow
	err := row.Scan(&p.id, &p.name, &p.slug, &p.description, &p.priceCents, &p.currency,
		&p.inventoryCount, &p.status, &p.metaTitle, &p.metaDescription, &p.createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.hydrate(ctx, tx, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// hydrate loads a row's images (gallery order) and categories.
func (s *Service) hydrate(ctx *fiber.Ctx, tx pgx.Tx, p *productRow) error {
	rows, err := tx.Query(ctx.Context(), `
		SELECT pi.id, m.url, m.alt_text, pi.sort_order
		FROM product_images pi
		JOIN media_assets m ON m.id = pi.media_asset_id
		WHERE pi.product_id = $1
		ORDER BY pi.sort_order, pi.id`, p.id)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var im imageRow
		if err := rows.Scan(&im.id, &im.url, &im.altText, &im.sortOrder); err != nil {
			return err
		}
		p.images = append(p.images, im)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	crows, err := tx.Query(ctx.Context(), `
		SELECT c.id, c.name, c.slug
		FROM product_categories pc
		JOIN categories c ON c.id = pc.category_id
		WHERE pc.product_id = $1
		ORDER BY c.name`, p.id)
	if err != nil {
		return err
	}
	defer crows.Close()
	for crows.Next() {
		var ct categoryRow
		if err := crows.Scan(&ct.id, &ct.name, &ct.slug); err != nil {
			return err
		}
		p.categories = append(p.categories, ct)
	}
	return crows.Err()
}

// --- request/response types -----------------------------------------------------

type createProductRequest struct {
	Name            string   `json:"name"`
	Slug            string   `json:"slug"`
	Description     string   `json:"description"`
	PriceCents      int      `json:"price_cents"`
	Currency        string   `json:"currency"`
	InventoryCount  int      `json:"inventory_count"`
	Status          string   `json:"status"`
	MetaTitle       string   `json:"meta_title"`
	MetaDescription string   `json:"meta_description"`
	CategoryIDs     []string `json:"category_ids"`
}

func validProductStatus(s string) bool {
	switch s {
	case "", "draft", "active", "archived":
		return true
	}
	return false
}

// --- handlers -------------------------------------------------------------------

// CreateProduct handles POST /products (admin). Creates the product and, if
// category_ids supplied, links them — all in the request RLS tx.
func (s *Service) CreateProduct(c *fiber.Ctx) error {
	var req createProductRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.Name == "" || req.Slug == "" || req.PriceCents < 0 {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "name, slug, price_cents required"})
	}
	status := req.Status
	if status == "" {
		status = "draft"
	}
	if !validProductStatus(status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}
	currency := req.Currency
	if currency == "" {
		currency = "usd"
	}
	tid := tenantID(c)
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()

	slug := req.Slug
	id := uuid.NewString()
	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, `
			INSERT INTO products (id, tenant_id, name, slug, description, price_cents,
				currency, inventory_count, status, meta_title, meta_description)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
			id, tid, req.Name, slug, nullableStr(req.Description), req.PriceCents,
			currency, req.InventoryCount, status, nullableStr(req.MetaTitle),
			nullableStr(req.MetaDescription))
		return err
	}); err != nil {
		if isUniqueViolation(err) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "slug taken"})
		}
		return fiber.ErrInternalServerError
	}

	// Link categories (validated by existence within RLS scope).
	for _, cid := range req.CategoryIDs {
		if err := savepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, `
				INSERT INTO product_categories (tenant_id, product_id, category_id)
				VALUES ($1,$2,$3)`, tid, id, cid)
			return err
		}); err != nil {
			return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unknown category id"})
		}
	}

	p, err := s.queryProduct(c, tx, "p.id = $1", id)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(productJSON(p))
}

func nullableStr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// ListProducts handles GET /products (public). Only active products, with
// optional tsvector search (?search=) and category slug filter (?category=).
func (s *Service) ListProducts(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()

	var args []any
	var conds []string
	conds = append(conds, "p.status = 'active'")

	if q := strings.TrimSpace(c.Query("search")); q != "" {
		args = append(args, q)
		conds = append(conds, fmt.Sprintf("p.search_vector @@ plainto_tsquery('english', $%d)", len(args)))
	}
	if cat := strings.TrimSpace(c.Query("category")); cat != "" {
		args = append(args, cat)
		conds = append(conds, fmt.Sprintf(`EXISTS (
			SELECT 1 FROM product_categories pc JOIN categories c ON c.id = pc.category_id
			WHERE pc.product_id = p.id AND c.slug = $%d)`, len(args)))
	}

	rows, err := tx.Query(ctx, fmt.Sprintf(`
		SELECT p.id, p.name, p.slug, p.description, p.price_cents, p.currency,
		       p.inventory_count, p.status, p.meta_title, p.meta_description, p.created_at
		FROM products p
		WHERE %s
		ORDER BY p.created_at DESC, p.id`, strings.Join(conds, " AND ")), args...)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	defer rows.Close()

	var found []productRow
	for rows.Next() {
		var p productRow
		if err := rows.Scan(&p.id, &p.name, &p.slug, &p.description, &p.priceCents, &p.currency,
			&p.inventoryCount, &p.status, &p.metaTitle, &p.metaDescription, &p.createdAt); err != nil {
			return fiber.ErrInternalServerError
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return fiber.ErrInternalServerError
	}
	rows.Close()

	// Hydrate images/categories outside the iterating cursor: pgx5 refuses a
	// second query on the same connection while rows is open ("conn busy").
	products := make([]fiber.Map, 0, len(found))
	for i := range found {
		if err := s.hydrate(c, tx, &found[i]); err != nil {
			return fiber.ErrInternalServerError
		}
		products = append(products, productJSON(&found[i]))
	}
	return c.JSON(fiber.Map{"products": products})
}

// GetProduct handles GET /products/:id (public: active only; admin: any status).
func (s *Service) GetProduct(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	where := "p.id = $1"
	if !isAdmin(c) {
		where += " AND p.status = 'active'"
	}
	p, err := s.queryProduct(c, tx, where, c.Params("id"))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if p == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "product not found"})
	}
	return c.JSON(productJSON(p))
}

// UpdateProduct handles PATCH /products/:id (admin).
func (s *Service) UpdateProduct(c *fiber.Ctx) error {
	var req createProductRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.Status != "" && !validProductStatus(req.Status) {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid status"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")
	tid := tenantID(c)

	// Ensure the product exists under this tenant before patching.
	if err := tx.QueryRow(ctx,
		"SELECT true FROM products WHERE id = $1", id).Scan(new(bool)); errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "product not found"})
	}

	var sets []string
	var args []any
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}

	if req.Name != "" {
		set("name", req.Name)
	}
	if req.Slug != "" {
		set("slug", req.Slug)
	}
	if req.Description != "" {
		set("description", nullableStr(req.Description))
	}
	if req.PriceCents >= 0 {
		set("price_cents", req.PriceCents)
	}
	if req.Currency != "" {
		set("currency", req.Currency)
	}
	if req.InventoryCount >= 0 {
		set("inventory_count", req.InventoryCount)
	}
	if req.Status != "" {
		set("status", req.Status)
	}
	if req.MetaTitle != "" {
		set("meta_title", nullableStr(req.MetaTitle))
	}
	if req.MetaDescription != "" {
		set("meta_description", nullableStr(req.MetaDescription))
	}

	if len(sets) > 0 {
		args = append(args, id)
		if err := savepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, fmt.Sprintf("UPDATE products SET %s WHERE id = $%d",
				strings.Join(sets, ", "), len(args)), args...)
			return err
		}); err != nil {
			if isUniqueViolation(err) {
				return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "slug taken"})
			}
			return fiber.ErrInternalServerError
		}
	}

	// Replace category links if category_ids provided.
	if req.CategoryIDs != nil {
		if _, err := tx.Exec(ctx,
			"DELETE FROM product_categories WHERE product_id = $1", id); err != nil {
			return fiber.ErrInternalServerError
		}
		for _, cid := range req.CategoryIDs {
			if err := savepoint(ctx, tx, func() error {
				_, err := tx.Exec(ctx, `
					INSERT INTO product_categories (tenant_id, product_id, category_id)
					VALUES ($1,$2,$3)`, tid, id, cid)
				return err
			}); err != nil {
				return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "unknown category id"})
			}
		}
	}

	p, err := s.queryProduct(c, tx, "p.id = $1", id)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if p == nil {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "product not found"})
	}
	return c.JSON(productJSON(p))
}

// DeleteProduct handles DELETE /products/:id (admin). Removes image/category
// links first (FKs), then the product — inside the request tx.
func (s *Service) DeleteProduct(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")

	var exists bool
	err := tx.QueryRow(ctx, "SELECT true FROM products WHERE id = $1", id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "product not found"})
	}
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM product_images WHERE product_id = $1", id); err != nil {
		return fiber.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM product_categories WHERE product_id = $1", id); err != nil {
		return fiber.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM products WHERE id = $1", id); err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"deleted": id})
}

// AddImage handles POST /products/:id/images (admin). media_asset_id must
// exist within this tenant's media library (RLS scopes the lookup).
func (s *Service) AddImage(c *fiber.Ctx) error {
	var req struct {
		MediaAssetID string `json:"media_asset_id"`
		SortOrder    int    `json:"sort_order"`
	}
	if err := c.BodyParser(&req); err != nil {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "invalid body"})
	}
	if req.MediaAssetID == "" {
		return c.Status(fiber.StatusBadRequest).JSON(fiber.Map{"error": "media_asset_id required"})
	}
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")
	tid := tenantID(c)

	var prodExists bool
	if err := tx.QueryRow(ctx, "SELECT true FROM products WHERE id = $1", id).Scan(&prodExists); errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "product not found"})
	}
	var assetExists bool
	if err := tx.QueryRow(ctx, "SELECT true FROM media_assets WHERE id = $1", req.MediaAssetID).Scan(&assetExists); errors.Is(err, pgx.ErrNoRows) {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "media asset not found"})
	}

	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, `
			INSERT INTO product_images (tenant_id, product_id, media_asset_id, sort_order)
			VALUES ($1,$2,$3,$4)`, tid, id, req.MediaAssetID, req.SortOrder)
		return err
	}); err != nil {
		if isUniqueViolation(err) {
			return c.Status(fiber.StatusConflict).JSON(fiber.Map{"error": "image already attached"})
		}
		return fiber.ErrBadRequest
	}

	p, err := s.queryProduct(c, tx, "p.id = $1", id)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(productJSON(p))
}

// RemoveImage handles DELETE /products/:id/images/:imageId (admin).
func (s *Service) RemoveImage(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	ctx := c.Context()
	tag, err := tx.Exec(ctx, `
		DELETE FROM product_images
		WHERE id = $1 AND product_id = $2`, c.Params("imageId"), c.Params("id"))
	if err != nil {
		return fiber.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return c.Status(fiber.StatusNotFound).JSON(fiber.Map{"error": "image not found"})
	}
	return c.JSON(fiber.Map{"deleted": c.Params("imageId")})
}

// ListCategories handles GET /categories (public).
func (s *Service) ListCategories(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return fiber.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(), `
		SELECT id, name, slug FROM categories
		ORDER BY name`)
	if err != nil {
		return fiber.ErrInternalServerError
	}
	defer rows.Close()
	var cats []fiber.Map
	for rows.Next() {
		var ct categoryRow
		if err := rows.Scan(&ct.id, &ct.name, &ct.slug); err != nil {
			return fiber.ErrInternalServerError
		}
		cats = append(cats, fiber.Map{"id": ct.id, "name": ct.name, "slug": ct.slug})
	}
	if err := rows.Err(); err != nil {
		return fiber.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"categories": cats})
}

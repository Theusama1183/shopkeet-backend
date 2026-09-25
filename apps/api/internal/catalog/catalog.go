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

	"github.com/shopkeet/api/internal/platform/httperr"
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

func isFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
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

// optionValueRow is a product option's selectable value.
type optionValueRow struct {
	id        string
	value     string
	sortOrder int
}

// optionRow is a product option (e.g. "Size") with its values.
type optionRow struct {
	id        string
	name      string
	sortOrder int
	values    []optionValueRow
}

// variantOptionRow is one entry of a variant's option-value mapping, joined
// with the option so the storefront can group by option.
type variantOptionRow struct {
	optionValueID string
	optionID      string
	optionName    string
	value         string
}

// variantRow is a concrete purchasable variant (price/stock/SKU) with its
// option-value mapping.
type variantRow struct {
	id             string
	sku            *string
	priceCents     int
	inventoryCount int
	weightGrams    *int
	status         string
	optionValues   []variantOptionRow
}

// productRow mirrors a products row plus its images/categories/options/variants.
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
	options         []optionRow
	variants        []variantRow
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
	opts := make([]fiber.Map, 0, len(p.options))
	for _, o := range p.options {
		vals := make([]fiber.Map, 0, len(o.values))
		for _, v := range o.values {
			vals = append(vals, fiber.Map{"id": v.id, "value": v.value, "sort_order": v.sortOrder})
		}
		opts = append(opts, fiber.Map{"id": o.id, "name": o.name, "sort_order": o.sortOrder, "values": vals})
	}
	variants := make([]fiber.Map, 0, len(p.variants))
	for _, v := range p.variants {
		links := make([]fiber.Map, 0, len(v.optionValues))
		for _, l := range v.optionValues {
			links = append(links, fiber.Map{
				"option_value_id": l.optionValueID, "option_id": l.optionID,
				"option_name": l.optionName, "value": l.value,
			})
		}
		variants = append(variants, fiber.Map{
			"id": v.id, "sku": strp(v.sku), "price_cents": v.priceCents,
			"inventory_count": v.inventoryCount, "weight_grams": v.weightGrams,
			"status": v.status, "option_values": links,
		})
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
		"options":          opts,
		"variants":         variants,
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
	if err := s.hydrateDetail(ctx, tx, &p); err != nil {
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

// hydrateDetail loads a row's options (with their values) and variants (with
// their option-value mapping). Only single-product responses (GET/POST/PATCH
// /products/:id) embed these; list responses stay light. Each section is a
// single query that is fully drained before the next section's query runs —
// pgx allows only one active result per transaction connection.
func (s *Service) hydrateDetail(ctx *fiber.Ctx, tx pgx.Tx, p *productRow) error {
	orows, err := tx.Query(ctx.Context(), `
		SELECT id, name, sort_order
		FROM product_options
		WHERE product_id = $1
		ORDER BY sort_order, id`, p.id)
	if err != nil {
		return err
	}
	var options []optionRow
	for orows.Next() {
		var o optionRow
		if err := orows.Scan(&o.id, &o.name, &o.sortOrder); err != nil {
			orows.Close()
			return err
		}
		options = append(options, o)
	}
	if err := orows.Err(); err != nil {
		return err
	}
	orows.Close()
	p.options = options

	if len(options) > 0 {
		vrows, err := tx.Query(ctx.Context(), `
			SELECT ov.option_id, ov.id, ov.value, ov.sort_order
			FROM product_option_values ov
			JOIN product_options o ON o.id = ov.option_id
			WHERE o.product_id = $1
			ORDER BY o.sort_order, ov.sort_order, ov.id`, p.id)
		if err != nil {
			return err
		}
		byOption := make(map[string]*optionRow, len(options))
		for i := range p.options {
			byOption[p.options[i].id] = &p.options[i]
		}
		for vrows.Next() {
			var oid, vid, val string
			var so int
			if err := vrows.Scan(&oid, &vid, &val, &so); err != nil {
				vrows.Close()
				return err
			}
			if o, ok := byOption[oid]; ok {
				o.values = append(o.values, optionValueRow{id: vid, value: val, sortOrder: so})
			}
		}
		if err := vrows.Err(); err != nil {
			return err
		}
		vrows.Close()
	}

	vr, err := tx.Query(ctx.Context(), `
		SELECT v.id, v.sku, v.price_cents, v.inventory_count, v.weight_grams, v.status
		FROM product_variants v
		WHERE v.product_id = $1
		ORDER BY v.created_at, v.id`, p.id)
	if err != nil {
		return err
	}
	var variants []variantRow
	for vr.Next() {
		var v variantRow
		if err := vr.Scan(&v.id, &v.sku, &v.priceCents, &v.inventoryCount, &v.weightGrams, &v.status); err != nil {
			vr.Close()
			return err
		}
		variants = append(variants, v)
	}
	if err := vr.Err(); err != nil {
		return err
	}
	vr.Close()
	p.variants = variants

	if len(variants) > 0 {
		lrows, err := tx.Query(ctx.Context(), `
			SELECT vov.variant_id, vov.option_value_id, ov.option_id, o.name, ov.value
			FROM product_variant_option_values vov
			JOIN product_variants v ON v.id = vov.variant_id
			JOIN product_option_values ov ON ov.id = vov.option_value_id
			JOIN product_options o ON o.id = ov.option_id
			WHERE v.product_id = $1
			ORDER BY o.sort_order, ov.sort_order, ov.id`, p.id)
		if err != nil {
			return err
		}
		byVariant := make(map[string]*variantRow, len(variants))
		for i := range p.variants {
			byVariant[p.variants[i].id] = &p.variants[i]
		}
		for lrows.Next() {
			var vid, ovalueID, oid, oname, val string
			if err := lrows.Scan(&vid, &ovalueID, &oid, &oname, &val); err != nil {
				lrows.Close()
				return err
			}
			if v, ok := byVariant[vid]; ok {
				v.optionValues = append(v.optionValues, variantOptionRow{
					optionValueID: ovalueID, optionID: oid, optionName: oname, value: val,
				})
			}
		}
		if err := lrows.Err(); err != nil {
			return err
		}
		lrows.Close()
	}
	return nil
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
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Name == "" || req.Slug == "" || req.PriceCents < 0 {
		return httperr.C(fiber.StatusBadRequest, "name, slug, price_cents required")
	}
	status := req.Status
	if status == "" {
		status = "draft"
	}
	if !validProductStatus(status) {
		return httperr.C(fiber.StatusBadRequest, "invalid status")
	}
	currency := req.Currency
	if currency == "" {
		currency = "usd"
	}
	tid := tenantID(c)
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
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
			return httperr.C(fiber.StatusConflict, "slug taken")
		}
		return httperr.ErrInternalServerError
	}

	// Link categories (validated by existence within RLS scope).
	for _, cid := range req.CategoryIDs {
		if err := savepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, `
				INSERT INTO product_categories (tenant_id, product_id, category_id)
				VALUES ($1,$2,$3)`, tid, id, cid)
			return err
		}); err != nil {
			return httperr.C(fiber.StatusBadRequest, "unknown category id")
		}
	}

	// Every product always has at least one variant (Phase 8 design rule): a
	// simple product gets one auto-created "Default" variant — no option
	// values, no SKU — carrying the flat product's price and stock. products
	// .price_cents/inventory_count cache it.
	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1,$2,$3,$4,$5)`,
			tid, id, req.PriceCents, req.InventoryCount, "active")
		return err
	}); err != nil {
		return httperr.ErrInternalServerError
	}

	p, err := s.queryProduct(c, tx, "p.id = $1", id)
	if err != nil {
		return httperr.ErrInternalServerError
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
		return httperr.ErrInternalServerError
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
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	var found []productRow
	for rows.Next() {
		var p productRow
		if err := rows.Scan(&p.id, &p.name, &p.slug, &p.description, &p.priceCents, &p.currency,
			&p.inventoryCount, &p.status, &p.metaTitle, &p.metaDescription, &p.createdAt); err != nil {
			return httperr.ErrInternalServerError
		}
		found = append(found, p)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()

	// Hydrate images/categories outside the iterating cursor: pgx5 refuses a
	// second query on the same connection while rows is open ("conn busy").
	products := make([]fiber.Map, 0, len(found))
	for i := range found {
		if err := s.hydrate(c, tx, &found[i]); err != nil {
			return httperr.ErrInternalServerError
		}
		products = append(products, productJSON(&found[i]))
	}
	return c.JSON(fiber.Map{"products": products})
}

// GetProduct handles GET /products/:id (public: active only; admin: any status).
func (s *Service) GetProduct(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	where := "p.id = $1"
	if !isAdmin(c) {
		where += " AND p.status = 'active'"
	}
	p, err := s.queryProduct(c, tx, where, c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if p == nil {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	return c.JSON(productJSON(p))
}

// UpdateProduct handles PATCH /products/:id (admin).
func (s *Service) UpdateProduct(c *fiber.Ctx) error {
	var req createProductRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Status != "" && !validProductStatus(req.Status) {
		return httperr.C(fiber.StatusBadRequest, "invalid status")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")
	tid := tenantID(c)

	// Ensure the product exists under this tenant before patching.
	if err := tx.QueryRow(ctx,
		"SELECT true FROM products WHERE id = $1", id).Scan(new(bool)); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "product not found")
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
				return httperr.C(fiber.StatusConflict, "slug taken")
			}
			return httperr.ErrInternalServerError
		}
	}

	// Replace category links if category_ids provided.
	if req.CategoryIDs != nil {
		if _, err := tx.Exec(ctx,
			"DELETE FROM product_categories WHERE product_id = $1", id); err != nil {
			return httperr.ErrInternalServerError
		}
		for _, cid := range req.CategoryIDs {
			if err := savepoint(ctx, tx, func() error {
				_, err := tx.Exec(ctx, `
					INSERT INTO product_categories (tenant_id, product_id, category_id)
					VALUES ($1,$2,$3)`, tid, id, cid)
				return err
			}); err != nil {
				return httperr.C(fiber.StatusBadRequest, "unknown category id")
			}
		}
	}

	// A product with exactly one variant (the flat "Default" variant, the only
	// shape produced by the pre-Phase-8 API) treats price/inventory patches as
	// edits to that variant, so the cached products row and the variant stay in
	// sync and simple-store behaviour is unchanged. Multi-variant products are
	// priced per-variant instead; the cached product value is recomputed on
	// every variant mutation.
	if req.PriceCents >= 0 || req.InventoryCount >= 0 {
		var variantCount int
		if err := tx.QueryRow(ctx,
			"SELECT count(*) FROM product_variants WHERE product_id = $1", id).Scan(&variantCount); err != nil {
			return httperr.ErrInternalServerError
		}
		if variantCount == 1 {
			var vsets []string
			var vargs []any
			if req.PriceCents >= 0 {
				vargs = append(vargs, req.PriceCents)
				vsets = append(vsets, fmt.Sprintf("price_cents = $%d", len(vargs)))
			}
			if req.InventoryCount >= 0 {
				vargs = append(vargs, req.InventoryCount)
				vsets = append(vsets, fmt.Sprintf("inventory_count = $%d", len(vargs)))
			}
			if len(vsets) > 0 {
				vargs = append(vargs, id)
				if err := savepoint(ctx, tx, func() error {
					_, err := tx.Exec(ctx, fmt.Sprintf(
						"UPDATE product_variants SET %s WHERE product_id = $%d",
						strings.Join(vsets, ", "), len(vargs)), vargs...)
					return err
				}); err != nil {
					return httperr.ErrInternalServerError
				}
			}
		}
	}

	p, err := s.queryProduct(c, tx, "p.id = $1", id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if p == nil {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	return c.JSON(productJSON(p))
}

// DeleteProduct handles DELETE /products/:id (admin). Removes image/category,
// variant and option structures first (FKs), then the product — inside the
// request tx. Deleting a product whose variants have order history fails on the
// order_items FK (409).
func (s *Service) DeleteProduct(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")

	var exists bool
	err := tx.QueryRow(ctx, "SELECT true FROM products WHERE id = $1", id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM product_variant_option_values vov USING product_variants v WHERE vov.variant_id = v.id AND v.product_id = $1",
		id); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM product_variants WHERE product_id = $1", id); err != nil {
		if isFKViolation(err) {
			return httperr.C(fiber.StatusConflict, "product referenced by carts or orders")
		}
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, `
		DELETE FROM product_option_values vov USING product_options o
		WHERE vov.option_id = o.id AND o.product_id = $1`, id); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM product_options WHERE product_id = $1", id); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM product_images WHERE product_id = $1", id); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM product_categories WHERE product_id = $1", id); err != nil {
		return httperr.ErrInternalServerError
	}
	if _, err := tx.Exec(ctx, "DELETE FROM products WHERE id = $1", id); err != nil {
		if isFKViolation(err) {
			return httperr.C(fiber.StatusConflict, "product referenced by cart_items or order_items")
		}
		return httperr.ErrInternalServerError
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
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.MediaAssetID == "" {
		return httperr.C(fiber.StatusBadRequest, "media_asset_id required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")
	tid := tenantID(c)

	var prodExists bool
	if err := tx.QueryRow(ctx, "SELECT true FROM products WHERE id = $1", id).Scan(&prodExists); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	var assetExists bool
	if err := tx.QueryRow(ctx, "SELECT true FROM media_assets WHERE id = $1", req.MediaAssetID).Scan(&assetExists); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "media asset not found")
	}

	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, `
			INSERT INTO product_images (tenant_id, product_id, media_asset_id, sort_order)
			VALUES ($1,$2,$3,$4)`, tid, id, req.MediaAssetID, req.SortOrder)
		return err
	}); err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "image already attached")
		}
		return httperr.BadRequest("bad_request", "bad request")
	}

	p, err := s.queryProduct(c, tx, "p.id = $1", id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(productJSON(p))
}

// RemoveImage handles DELETE /products/:id/images/:imageId (admin).
func (s *Service) RemoveImage(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tag, err := tx.Exec(ctx, `
		DELETE FROM product_images
		WHERE id = $1 AND product_id = $2`, c.Params("imageId"), c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "image not found")
	}
	return c.JSON(fiber.Map{"deleted": c.Params("imageId")})
}

// ListCategories handles GET /categories (public).
func (s *Service) ListCategories(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(), `
		SELECT id, name, slug FROM categories
		ORDER BY name`)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	var cats []fiber.Map
	for rows.Next() {
		var ct categoryRow
		if err := rows.Scan(&ct.id, &ct.name, &ct.slug); err != nil {
			return httperr.ErrInternalServerError
		}
		cats = append(cats, fiber.Map{"id": ct.id, "name": ct.name, "slug": ct.slug})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"categories": cats})
}

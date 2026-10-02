package bundles

import (
	"errors"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Service implements the Phase 25 admin CRUD + public storefront listing.
// Every query runs inside the RLS-scoped request transaction opened by the
// middleware (c.Locals("tx")), so tenant isolation is guaranteed for free.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

// --- JSON shape ---------------------------------------------------------------

type bundleItemJSON struct {
	ID         string `json:"id"`
	ProductID  string `json:"product_id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	PriceCents int    `json:"price_cents"`
	Quantity   int    `json:"quantity"`
}

const bundleItemsQuery = `
	SELECT bi.id, bi.product_id, bi.quantity, p.name, p.slug, p.price_cents
	FROM bundle_items bi
	JOIN products p ON p.id = bi.product_id
	WHERE bi.bundle_id = $1
	ORDER BY p.name`

func loadBundleItemRows(c *fiber.Ctx, tx pgx.Tx, bundleID string) ([]bundleItemJSON, error) {
	rows, err := tx.Query(c.Context(), bundleItemsQuery, bundleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var items []bundleItemJSON
	for rows.Next() {
		var it bundleItemJSON
		if err := rows.Scan(&it.ID, &it.ProductID, &it.Quantity, &it.Name, &it.Slug, &it.PriceCents); err != nil {
			return nil, err
		}
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}

func bundleJSON(id, name, btype, status string, priceCents, discountPercent *int,
	createdAt string, items []bundleItemJSON, public bool) fiber.Map {

	itemMaps := make([]fiber.Map, 0, len(items))
	for _, it := range items {
		itemMaps = append(itemMaps, fiber.Map{
			"id": it.ID, "product_id": it.ProductID, "name": it.Name, "slug": it.Slug,
			"price_cents": it.PriceCents, "quantity": it.Quantity,
		})
	}
	m := fiber.Map{
		"id": id, "name": name, "type": btype,
		"status": status, "created_at": createdAt, "items": itemMaps,
	}
	if priceCents != nil {
		m["bundle_price_cents"] = *priceCents
	} else {
		m["bundle_price_cents"] = nil
	}
	if discountPercent != nil {
		m["discount_percent"] = *discountPercent
	} else {
		m["discount_percent"] = nil
	}
	if public {
		delete(m, "status")
	}
	return m
}

// --- admin: bundles CRUD --------------------------------------------------------

// ListBundles handles GET /bundles (Admin). Returns the tenant's bundles with
// their items, newest first, optionally filtered by ?status=.
func (s *Service) ListBundles(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	where, args := "1 = 1", []any{}
	if st := c.Query("status"); st != "" {
		where, args = "status = $1", []any{st}
	}
	rows, err := tx.Query(ctx, `
		SELECT id, name, type, bundle_price_cents, discount_percent, status, created_at
		FROM bundles WHERE `+where+` ORDER BY created_at DESC, id`, args...)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	type baseRow struct {
		id, name, btype, status string
		priceCents              *int
		discountPercent         *int
		createdAt               string
	}
	var found []baseRow
	for rows.Next() {
		var b baseRow
		if err := rows.Scan(&b.id, &b.name, &b.btype, &b.priceCents, &b.discountPercent,
			&b.status, &b.createdAt); err != nil {
			rows.Close()
			return httperr.ErrInternalServerError
		}
		found = append(found, b)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()

	out := make([]fiber.Map, 0, len(found))
	for _, b := range found {
		items, err := loadBundleItemRows(c, tx, b.id)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		out = append(out, bundleJSON(b.id, b.name, b.btype, b.status, b.priceCents,
			b.discountPercent, b.createdAt, items, false))
	}
	return c.JSON(fiber.Map{"bundles": out})
}

type bundleItemInput struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

type createBundleRequest struct {
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	BundlePriceCents *int              `json:"bundle_price_cents"`
	DiscountPercent  *int              `json:"discount_percent"`
	Status           string            `json:"status"`
	Items            []bundleItemInput `json:"items"`
}

// hasActiveProduct reports whether the product exists and is sellable. A
// missing or archived product both map to the same "fix your payload" error.
func (s *Service) hasActiveProduct(c *fiber.Ctx, tx pgx.Tx, productID string) (bool, error) {
	err := tx.QueryRow(c.Context(),
		"SELECT 1 FROM products WHERE id = $1 AND status = 'active'", productID).Scan(new(int))
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func validateBundle(btype string, priceCents, discountPercent *int, status string, items []bundleItemInput) error {
	if btype != "fixed" && btype != "mix_and_match" {
		return errors.New("type must be 'fixed' or 'mix_and_match'")
	}
	if status != "" && status != "draft" && status != "active" && status != "archived" {
		return errors.New("status must be draft, active, or archived")
	}
	if (priceCents == nil) == (discountPercent == nil) {
		if priceCents == nil {
			return errors.New("exactly one of bundle_price_cents or discount_percent is required")
		}
		return errors.New("set exactly one of bundle_price_cents or discount_percent, not both")
	}
	if priceCents != nil && *priceCents <= 0 {
		return errors.New("bundle_price_cents must be positive")
	}
	if discountPercent != nil && (*discountPercent < 1 || *discountPercent > 100) {
		return errors.New("discount_percent must be between 1 and 100")
	}
	if btype == "mix_and_match" && discountPercent == nil {
		return errors.New("mix_and_match bundles require discount_percent")
	}
	if len(items) == 0 {
		return errors.New("at least one item is required")
	}
	return nil
}

// prodID returns the canonical ProductID of an item, defaulting to "" when the
// caller left it empty (the route id-based add below).
func prodID(it bundleItemInput) string { return it.ProductID }

// CreateBundle handles POST /bundles (Admin). Creates the bundle and its items
// atomically inside the request tx; any item that isn't an active product of
// the tenant aborts the whole insert (FK + RLS scope that).
func (s *Service) CreateBundle(c *fiber.Ctx) error {
	var req createBundleRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		return httperr.C(fiber.StatusBadRequest, "name required")
	}
	req.Type = strings.ToLower(strings.TrimSpace(req.Type))
	if err := validateBundle(req.Type, req.BundlePriceCents, req.DiscountPercent, req.Status, req.Items); err != nil {
		return httperr.C(fiber.StatusBadRequest, err.Error())
	}
	if req.Status == "" {
		req.Status = "draft"
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)

	for _, it := range req.Items {
		ok, err := s.hasActiveProduct(c, tx, prodID(it))
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if !ok {
			return httperr.C(fiber.StatusBadRequest, "item product not found or not active")
		}
	}

	var bundleID, createdAt string
	if err := tx.QueryRow(ctx, `
		INSERT INTO bundles (tenant_id, name, type, bundle_price_cents, discount_percent, status)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id, created_at::text`,
		tid, req.Name, req.Type, req.BundlePriceCents, req.DiscountPercent, req.Status).
		Scan(&bundleID, &createdAt); err != nil {
		return translateDBErr(err)
	}
	for _, it := range req.Items {
		quantity := it.Quantity
		if quantity < 1 {
			quantity = 1
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO bundle_items (tenant_id, bundle_id, product_id, quantity)
			VALUES ($1, $2, $3, $4)`, tid, bundleID, prodID(it), quantity); err != nil {
			return translateDBErr(err)
		}
	}

	items, err := loadBundleItemRows(c, tx, bundleID)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.Status(fiber.StatusCreated).JSON(bundleJSON(bundleID, req.Name, req.Type,
		req.Status, req.BundlePriceCents, req.DiscountPercent, createdAt, items, false))
}

// GetBundle handles GET /bundles/:id (Admin).
func (s *Service) GetBundle(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	var id, name, btype, status, createdAt string
	var priceCents, discountPercent *int
	err := tx.QueryRow(c.Context(), `
		SELECT id, name, type, bundle_price_cents, discount_percent, status, created_at::text
		FROM bundles WHERE id = $1`, c.Params("id")).
		Scan(&id, &name, &btype, &priceCents, &discountPercent, &status, &createdAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "bundle not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	items, err := loadBundleItemRows(c, tx, id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(bundleJSON(id, name, btype, status, priceCents, discountPercent, createdAt, items, false))
}

type updateBundleRequest struct {
	Name             *string            `json:"name"`
	Type             *string            `json:"type"`
	BundlePriceCents *int               `json:"bundle_price_cents"`
	DiscountPercent  *int               `json:"discount_percent"`
	Status           *string            `json:"status"`
	Items            *[]bundleItemInput `json:"items"`
}

// UpdateBundle handles PATCH /bundles/:id (Admin). Partial merge: only the
// fields present in the body change; sending a pricing field flips the pricing
// mode to it (exactly-one-of is re-validated on the merged row). Sending items
// replaces the bundle's items wholesale.
func (s *Service) UpdateBundle(c *fiber.Ctx) error {
	var req updateBundleRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)

	var id, name, btype, status, createdAt string
	var priceCents, discountPercent *int
	if err := tx.QueryRow(ctx, `
		SELECT id, name, type, bundle_price_cents, discount_percent, status, created_at::text
		FROM bundles WHERE id = $1 FOR UPDATE`, c.Params("id")).
		Scan(&id, &name, &btype, &priceCents, &discountPercent, &status, &createdAt); errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "bundle not found")
	} else if err != nil {
		return httperr.ErrInternalServerError
	}

	if req.Name != nil {
		if name = strings.TrimSpace(*req.Name); name == "" {
			return httperr.C(fiber.StatusBadRequest, "name required")
		}
	}
	if req.Type != nil {
		btype = strings.ToLower(strings.TrimSpace(*req.Type))
	}
	if req.BundlePriceCents != nil && req.DiscountPercent != nil {
		return httperr.C(fiber.StatusBadRequest,
			"set exactly one of bundle_price_cents or discount_percent, not both")
	}
	if req.BundlePriceCents != nil {
		priceCents, discountPercent = req.BundlePriceCents, nil
	}
	if req.DiscountPercent != nil {
		discountPercent, priceCents = req.DiscountPercent, nil
	}
	if req.Status != nil {
		status = *req.Status
	}
	var newItems []bundleItemInput
	if req.Items != nil {
		newItems = *req.Items
	} else {
		// Merge preserves the existing item set when the body omits it.
		existing, err := loadBundleItemRows(c, tx, id)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		newItems = make([]bundleItemInput, 0, len(existing))
		for _, it := range existing {
			newItems = append(newItems, bundleItemInput{ProductID: it.ProductID, Quantity: it.Quantity})
		}
	}
	if err := validateBundle(btype, priceCents, discountPercent, status, newItems); err != nil {
		return httperr.C(fiber.StatusBadRequest, err.Error())
	}
	for _, it := range newItems {
		ok, err := s.hasActiveProduct(c, tx, prodID(it))
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if !ok {
			return httperr.C(fiber.StatusBadRequest, "item product not found or not active")
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE bundles SET name = $1, type = $2, bundle_price_cents = $3,
			discount_percent = $4, status = $5 WHERE id = $6`,
		name, btype, priceCents, discountPercent, status, id); err != nil {
		return translateDBErr(err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM bundle_items WHERE bundle_id = $1", id); err != nil {
		return httperr.ErrInternalServerError
	}
	for _, it := range newItems {
		quantity := it.Quantity
		if quantity < 1 {
			quantity = 1
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO bundle_items (tenant_id, bundle_id, product_id, quantity)
			VALUES ($1, $2, $3, $4)`, tid, id, prodID(it), quantity); err != nil {
			return translateDBErr(err)
		}
	}

	items, err := loadBundleItemRows(c, tx, id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(bundleJSON(id, name, btype, status, priceCents, discountPercent, createdAt, items, false))
}

// DeleteBundle handles DELETE /bundles/:id (Admin). Referenced bundles (lines
// in a past or live order, or items sitting in a cart) cannot be deleted — the
// FK produces 23503, which we surface as a 409 so merchants don't destroy
// order history or active carts.
func (s *Service) DeleteBundle(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), "DELETE FROM bundles WHERE id = $1", c.Params("id"))
	if err != nil {
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == "23503" {
			return httperr.C(fiber.StatusConflict, "bundle is in use and cannot be deleted")
		}
		return httperr.ErrInternalServerError
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "bundle not found")
	}
	return c.JSON(fiber.Map{"deleted": true})
}

// --- public storefront ---------------------------------------------------------

// ListPublicBundles handles GET /bundles (Public). Active bundles only, with
// their items, for the X-Tenant-ID header storefront.
func (s *Service) ListPublicBundles(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	rows, err := tx.Query(ctx, `
		SELECT id, name, type, bundle_price_cents, discount_percent, status, created_at::text
		FROM bundles WHERE status = 'active' ORDER BY created_at DESC, id`)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	type baseRow struct {
		id, name, btype, status, createdAt string
		priceCents, discountPercent        *int
	}
	var found []baseRow
	for rows.Next() {
		var b baseRow
		if err := rows.Scan(&b.id, &b.name, &b.btype, &b.priceCents, &b.discountPercent,
			&b.status, &b.createdAt); err != nil {
			rows.Close()
			return httperr.ErrInternalServerError
		}
		found = append(found, b)
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	rows.Close()

	out := make([]fiber.Map, 0, len(found))
	for _, b := range found {
		items, err := loadBundleItemRows(c, tx, b.id)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		out = append(out, bundleJSON(b.id, b.name, b.btype, b.status, b.priceCents,
			b.discountPercent, b.createdAt, items, true))
	}
	return c.JSON(fiber.Map{"bundles": out})
}

// --- admin: quantity breaks -----------------------------------------------------

// productExists checks the route's product id resolves inside the tenant (RLS).
func (s *Service) productExists(c *fiber.Ctx, tx pgx.Tx, productID string) (bool, error) {
	var n int
	err := tx.QueryRow(c.Context(),
		"SELECT COUNT(*) FROM products WHERE id = $1", productID).Scan(&n)
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// ListQuantityBreaks handles GET /products/:id/quantity-breaks (Admin).
func (s *Service) ListQuantityBreaks(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ok, err := s.productExists(c, tx, c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if !ok {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	rows, err := tx.Query(c.Context(), `
		SELECT id, product_id, min_quantity, discount_percent
		FROM quantity_breaks WHERE product_id = $1 ORDER BY min_quantity`, c.Params("id"))
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()
	breaks := make([]fiber.Map, 0)
	for rows.Next() {
		var id, pid string
		var minQty, pct int
		if err := rows.Scan(&id, &pid, &minQty, &pct); err != nil {
			return httperr.ErrInternalServerError
		}
		breaks = append(breaks, fiber.Map{
			"id": id, "product_id": pid, "min_quantity": minQty, "discount_percent": pct,
		})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(fiber.Map{"quantity_breaks": breaks})
}

type quantityBreakRequest struct {
	MinQuantity     *int `json:"min_quantity"`
	DiscountPercent *int `json:"discount_percent"`
}

func validateBreak(minQty, pct *int) error {
	if minQty == nil || *minQty < 1 {
		return errors.New("min_quantity (>=1) required")
	}
	if pct == nil || *pct < 1 || *pct > 100 {
		return errors.New("discount_percent (1-100) required")
	}
	return nil
}

// CreateQuantityBreak handles POST /products/:id/quantity-breaks (Admin).
func (s *Service) CreateQuantityBreak(c *fiber.Ctx) error {
	var req quantityBreakRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if err := validateBreak(req.MinQuantity, req.DiscountPercent); err != nil {
		return httperr.C(fiber.StatusBadRequest, err.Error())
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)

	if ok, err := s.productExists(c, tx, c.Params("id")); err != nil {
		return httperr.ErrInternalServerError
	} else if !ok {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}

	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO quantity_breaks (tenant_id, product_id, min_quantity, discount_percent)
		VALUES ($1, $2, $3, $4) RETURNING id`,
		tid, c.Params("id"), *req.MinQuantity, *req.DiscountPercent).Scan(&id); err != nil {
		return translateDBErr(err)
	}
	return c.Status(fiber.StatusCreated).JSON(fiber.Map{
		"id": id, "product_id": c.Params("id"),
		"min_quantity": *req.MinQuantity, "discount_percent": *req.DiscountPercent,
	})
}

// UpdateQuantityBreak handles PATCH /products/:id/quantity-breaks/:breakID (Admin).
func (s *Service) UpdateQuantityBreak(c *fiber.Ctx) error {
	var req quantityBreakRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.MinQuantity == nil && req.DiscountPercent == nil {
		return httperr.C(fiber.StatusBadRequest, "nothing to update")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()

	var minQty, pct int
	err := tx.QueryRow(ctx, `
		SELECT min_quantity, discount_percent FROM quantity_breaks
		WHERE id = $1 AND product_id = $2 FOR UPDATE`,
		c.Params("breakID"), c.Params("id")).Scan(&minQty, &pct)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "quantity break not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if req.MinQuantity != nil {
		minQty = *req.MinQuantity
	}
	if req.DiscountPercent != nil {
		pct = *req.DiscountPercent
	}
	if err := validateBreak(&minQty, &pct); err != nil {
		return httperr.C(fiber.StatusBadRequest, err.Error())
	}
	if _, err := tx.Exec(ctx, `
		UPDATE quantity_breaks SET min_quantity = $1, discount_percent = $2
		WHERE id = $3`, minQty, pct, c.Params("breakID")); err != nil {
		return translateDBErr(err)
	}
	return c.JSON(fiber.Map{})
}

// DeleteQuantityBreak handles DELETE /products/:id/quantity-breaks/:breakID (Admin).
func (s *Service) DeleteQuantityBreak(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	tag, err := tx.Exec(c.Context(), `
		DELETE FROM quantity_breaks WHERE id = $1 AND product_id = $2`,
		c.Params("breakID"), c.Params("id"))
	if err != nil {
		return translateDBErr(err)
	}
	if tag.RowsAffected() == 0 {
		return httperr.C(fiber.StatusNotFound, "quantity break not found")
	}
	return c.JSON(fiber.Map{"deleted": true})
}

// translateDBErr maps common constraint violations to API errors; anything else
// is a 500.
func translateDBErr(err error) error {
	var pg *pgconn.PgError
	if errors.As(err, &pg) {
		switch pg.Code {
		case "23505":
			return httperr.C(fiber.StatusConflict, "that value already exists")
		case "23503":
			return httperr.C(fiber.StatusBadRequest, "referenced record does not exist")
		case "23514":
			return httperr.C(fiber.StatusBadRequest, "invalid bundle configuration")
		}
	}
	return httperr.ErrInternalServerError
}

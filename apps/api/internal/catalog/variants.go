package catalog

// Phase 8 (docs/07-expansion-build-spec.md): product options, option values
// and concrete variants. Handlers run inside the request RLS transaction.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/shopkeet/api/internal/platform/httperr"
)

var errOptionValueNotInProduct = errors.New("option value does not belong to this product")

// request types ----------------------------------------------------------------

type optionValueRequest struct {
	Value     string `json:"value"`
	SortOrder int    `json:"sort_order"`
}

type createOptionRequest struct {
	Name      string               `json:"name"`
	SortOrder int                  `json:"sort_order"`
	Values    []optionValueRequest `json:"values"`
}

type createVariantRequest struct {
	OptionValueIDs []string `json:"option_value_ids"`
	SKU            string   `json:"sku"`
	PriceCents     int      `json:"price_cents"`
	InventoryCount int      `json:"inventory_count"`
	WeightGrams    int      `json:"weight_grams"`
	Status         string   `json:"status"`
}

// updateVariantRequest uses pointers for SKU/weight_grams so the caller can
// distinguish "unchanged" (nil) from "clear to NULL" (empty/0).
type updateVariantRequest struct {
	OptionValueIDs []string `json:"option_value_ids"`
	SKU            *string  `json:"sku"`
	PriceCents     int      `json:"price_cents"`
	InventoryCount int      `json:"inventory_count"`
	WeightGrams    *int     `json:"weight_grams"`
	Status         string   `json:"status"`
}

func validVariantStatus(s string) bool {
	switch s {
	case "", "active", "archived":
		return true
	}
	return false
}

func nullableWeight(w int) *int {
	if w <= 0 {
		return nil
	}
	return &w
}

// recomputeCache refreshes products.price_cents (MIN active variant price) and
// products.inventory_count (SUM active variant stock). Called after every
// variant mutation and on checkout. products.price_cents/inventory_count are
// cached display values (docs/07-expansion-build-spec.md Phase 8). It also
// invalidates the Redis product-detail cache (Phase 14) so public reads
// repopulate with the refreshed aggregates.
func (s *Service) recomputeCache(ctx context.Context, tx pgx.Tx, tid, productID string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE products p SET
			price_cents = COALESCE((SELECT MIN(price_cents) FROM product_variants v
				WHERE v.product_id = p.id AND v.status = 'active'), 0),
			inventory_count = COALESCE((SELECT SUM(inventory_count) FROM product_variants v
				WHERE v.product_id = p.id AND v.status = 'active'), 0)
		WHERE p.id = $1`, productID); err != nil {
		return err
	}
	s.invalidateProduct(ctx, tid, productID)
	return nil
}

func (s *Service) productExists(ctx *fiber.Ctx, tx pgx.Tx, id string) (bool, error) {
	var exists bool
	err := tx.QueryRow(ctx.Context(), "SELECT true FROM products WHERE id = $1", id).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// validateOptionValueIDs confirms every id belongs to an option of this product
// (RLS additionally scopes to the tenant). All-or-nothing.
func (s *Service) validateOptionValueIDs(ctx context.Context, tx pgx.Tx, productID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM product_option_values ov
		JOIN product_options o ON o.id = ov.option_id
		WHERE o.product_id = $1 AND ov.id = ANY($2::uuid[])`, productID, ids).Scan(&n); err != nil {
		return err
	}
	if n != len(ids) {
		return errOptionValueNotInProduct
	}
	return nil
}

// respondProduct reloads the product detail and writes it, mapping internal
// errors to HTTP. Kept small: an error here is only ever an internal one.
func (s *Service) respondProduct(c *fiber.Ctx, tx pgx.Tx, id string) error {
	p, err := s.queryProduct(c, tx, "p.id = $1", id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if p == nil {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	return c.JSON(productJSON(p))
}

// --- option handlers -----------------------------------------------------------

// CreateOption handles POST /products/:id/options (admin). Option values that
// reference the same product option are created with it in one call.
func (s *Service) CreateOption(c *fiber.Ctx) error {
	var req createOptionRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Name == "" {
		return httperr.C(fiber.StatusBadRequest, "name required")
	}
	tid := tenantID(c)
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")

	exists, err := s.productExists(c, tx, id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if !exists {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}

	optionID := uuid.NewString()
	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, `
			INSERT INTO product_options (id, tenant_id, product_id, name, sort_order)
			VALUES ($1,$2,$3,$4,$5)`, optionID, tid, id, req.Name, req.SortOrder)
		return err
	}); err != nil {
		return httperr.ErrInternalServerError
	}

	for i, v := range req.Values {
		if strings.TrimSpace(v.Value) == "" {
			return httperr.C(fiber.StatusBadRequest, "option value text required")
		}
		order := v.SortOrder
		if i > 0 && v.SortOrder == 0 {
			order = i
		}
		if err := savepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, `
				INSERT INTO product_option_values (tenant_id, option_id, value, sort_order)
				VALUES ($1,$2,$3,$4)`, tid, optionID, v.Value, order)
			return err
		}); err != nil {
			return httperr.ErrInternalServerError
		}
	}

	s.invalidateProduct(ctx, tid, id)
	return s.respondProduct(c, tx, id)
}

// --- variant handlers ------------------------------------------------------------

// CreateVariant handles POST /products/:id/variants (admin). Option value ids
// must belong to this product's options; an empty set creates a bare "add-on"
// variant. On success the cached product price/stock is recomputed.
func (s *Service) CreateVariant(c *fiber.Ctx) error {
	var req createVariantRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.PriceCents < 0 {
		return httperr.C(fiber.StatusBadRequest, "price_cents required")
	}
	status := req.Status
	if status == "" {
		status = "active"
	}
	if !validVariantStatus(status) {
		return httperr.C(fiber.StatusBadRequest, "invalid status")
	}
	tid := tenantID(c)
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	id := c.Params("id")

	exists, err := s.productExists(c, tx, id)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	if !exists {
		return httperr.C(fiber.StatusNotFound, "product not found")
	}
	if err := s.validateOptionValueIDs(ctx, tx, id, req.OptionValueIDs); err != nil {
		if errors.Is(err, errOptionValueNotInProduct) {
			return httperr.C(fiber.StatusBadRequest, "option_value_ids do not belong to this product")
		}
		return httperr.ErrInternalServerError
	}

	variantID := uuid.NewString()
	if err := savepoint(ctx, tx, func() error {
		_, err := tx.Exec(ctx, `
			INSERT INTO product_variants (id, tenant_id, product_id, sku, price_cents, inventory_count, weight_grams, status)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			variantID, tid, id, nullableStr(req.SKU), req.PriceCents, req.InventoryCount,
			nullableWeight(req.WeightGrams), status)
		return err
	}); err != nil {
		if isUniqueViolation(err) {
			return httperr.C(fiber.StatusConflict, "sku taken")
		}
		return httperr.ErrInternalServerError
	}

	if err := linkOptionValues(ctx, tx, tid, variantID, req.OptionValueIDs); err != nil {
		return httperr.ErrInternalServerError
	}
	if err := s.recomputeCache(ctx, tx, tid, id); err != nil {
		return httperr.ErrInternalServerError
	}

	return s.respondProduct(c, tx, id)
}

// UpdateVariant handles PATCH /products/:id/variants/:variantId (admin).
// Fields are optional; option_value_ids replaced when provided.
func (s *Service) UpdateVariant(c *fiber.Ctx) error {
	var req updateVariantRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.Status != "" && !validVariantStatus(req.Status) {
		return httperr.C(fiber.StatusBadRequest, "invalid status")
	}
	tid := tenantID(c)
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	productID := c.Params("id")
	variantID := c.Params("variantId")

	var exists bool
	err := tx.QueryRow(ctx,
		"SELECT true FROM product_variants WHERE id = $1 AND product_id = $2",
		variantID, productID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "variant not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	var sets []string
	var args []any
	set := func(col string, v any) {
		args = append(args, v)
		sets = append(sets, fmt.Sprintf("%s = $%d", col, len(args)))
	}
	if req.SKU != nil {
		set("sku", nullableStr(*req.SKU))
	}
	if req.PriceCents >= 0 {
		set("price_cents", req.PriceCents)
	}
	if req.InventoryCount >= 0 {
		set("inventory_count", req.InventoryCount)
	}
	if req.WeightGrams != nil {
		set("weight_grams", nullableWeight(*req.WeightGrams))
	}
	if req.Status != "" {
		set("status", req.Status)
	}
	if len(sets) > 0 {
		args = append(args, variantID)
		if err := savepoint(ctx, tx, func() error {
			_, err := tx.Exec(ctx, fmt.Sprintf(
				"UPDATE product_variants SET %s WHERE id = $%d", strings.Join(sets, ", "), len(args)), args...)
			return err
		}); err != nil {
			if isUniqueViolation(err) {
				return httperr.C(fiber.StatusConflict, "sku taken")
			}
			return httperr.ErrInternalServerError
		}
	}

	if req.OptionValueIDs != nil {
		if err := s.validateOptionValueIDs(ctx, tx, productID, req.OptionValueIDs); err != nil {
			if errors.Is(err, errOptionValueNotInProduct) {
				return httperr.C(fiber.StatusBadRequest, "option_value_ids do not belong to this product")
			}
			return httperr.ErrInternalServerError
		}
		if _, err := tx.Exec(ctx,
			"DELETE FROM product_variant_option_values WHERE variant_id = $1", variantID); err != nil {
			return httperr.ErrInternalServerError
		}
		if err := linkOptionValues(ctx, tx, tid, variantID, req.OptionValueIDs); err != nil {
			return httperr.ErrInternalServerError
		}
	}

	if err := s.recomputeCache(ctx, tx, tid, productID); err != nil {
		return httperr.ErrInternalServerError
	}

	return s.respondProduct(c, tx, productID)
}

// DeleteVariant handles DELETE /products/:id/variants/:variantId (admin). The
// last remaining variant cannot be deleted (every product keeps >=1); variants
// referenced by carts or orders are protected with 409.
func (s *Service) DeleteVariant(c *fiber.Ctx) error {
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	productID := c.Params("id")
	variantID := c.Params("variantId")

	tid := tenantID(c)
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT true FROM product_variants WHERE id = $1 AND product_id = $2 AND tenant_id = $3`,
		variantID, productID, tid).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return httperr.C(fiber.StatusNotFound, "variant not found")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}

	var variantCount int
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM product_variants WHERE product_id = $1", productID).Scan(&variantCount); err != nil {
		return httperr.ErrInternalServerError
	}
	if variantCount <= 1 {
		return httperr.C(fiber.StatusBadRequest, "cannot delete the only variant")
	}

	if err := savepoint(ctx, tx, func() error {
		if _, err := tx.Exec(ctx,
			"DELETE FROM product_variant_option_values WHERE variant_id = $1", variantID); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, "DELETE FROM product_variants WHERE id = $1", variantID)
		return err
	}); err != nil {
		if isFKViolation(err) {
			return httperr.C(fiber.StatusConflict, "variant referenced by carts or orders")
		}
		return httperr.ErrInternalServerError
	}

	if err := s.recomputeCache(ctx, tx, tid, productID); err != nil {
		return httperr.ErrInternalServerError
	}

	return s.respondProduct(c, tx, productID)
}

func linkOptionValues(ctx context.Context, tx pgx.Tx, tid, variantID string, ids []string) error {
	for _, ovid := range ids {
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_variant_option_values (tenant_id, variant_id, option_value_id)
			VALUES ($1,$2,$3)`, tid, variantID, ovid); err != nil {
			return err
		}
	}
	return nil
}

package cart

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"

	"github.com/shopkeet/api/internal/bundles"
	"github.com/shopkeet/api/internal/platform/httperr"
)

type bundleSelection struct {
	ProductID string `json:"product_id"`
	Quantity  int    `json:"quantity"`
}

type addBundleRequest struct {
	BundleID   string            `json:"bundle_id"`
	Quantity   int               `json:"quantity"`
	Selections []bundleSelection `json:"selections"`
}

// AddBundle handles POST /cart/bundle (Customer, Phase 25). A 'fixed' bundle
// expands into one cart_items row per component variant, each tagged with the
// bundle's id and the cart-level quantity times the component's per-bundle
// quantity; a 'mix_and_match' bundle expands the customer's selections the same
// way. The cart prices the whole bundle (bundles.PriceCart), not the sum of
// the individual variants. Bundle variants are resolved to the product's
// cheapest active variant; a component already sitting in the cart outright is
// refused rather than merged, so line-level pricing never gets corrupted.
func (s *Service) AddBundle(c *fiber.Ctx) error {
	var req addBundleRequest
	if err := c.BodyParser(&req); err != nil {
		return httperr.C(fiber.StatusBadRequest, "invalid body")
	}
	if req.BundleID == "" {
		return httperr.C(fiber.StatusBadRequest, "bundle_id required")
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	ctx := c.Context()
	tid, _ := c.Locals("tenant_id").(string)
	session := customerSession(c)

	b, err := bundles.ActiveBundle(ctx, tx, req.BundleID)
	if errors.Is(err, bundles.ErrNotFound) {
		return httperr.C(fiber.StatusNotFound, "bundle not found")
	}
	if errors.Is(err, bundles.ErrNotActive) {
		return httperr.C(fiber.StatusConflict, "bundle is not available")
	}
	if err != nil {
		return httperr.ErrInternalServerError
	}
	items, err := bundles.BundleItems(ctx, tx, req.BundleID)
	if err != nil {
		return httperr.ErrInternalServerError
	}

	type line struct {
		productID string
		variantID string
		quantity  int
	}
	var lines []line
	switch b.Type {
	case "fixed":
		if req.Quantity < 1 || req.Quantity > 99 {
			return httperr.C(fiber.StatusBadRequest, "quantity (1-99) required")
		}
		for pid, need := range items {
			vid, err := bundles.DefaultVariant(ctx, tx, pid)
			if errors.Is(err, pgx.ErrNoRows) {
				return httperr.C(fiber.StatusConflict, "a bundle product is no longer available")
			}
			if err != nil {
				return httperr.ErrInternalServerError
			}
			lines = append(lines, line{productID: pid, variantID: vid, quantity: req.Quantity * need})
		}
		if len(lines) == 0 {
			return httperr.C(fiber.StatusConflict, "bundle has no available products")
		}
	case "mix_and_match":
		if len(req.Selections) == 0 {
			return httperr.C(fiber.StatusBadRequest, "selections required")
		}
		for _, sel := range req.Selections {
			if _, inPool := items[sel.ProductID]; !inPool {
				return httperr.C(fiber.StatusBadRequest, "selection is not part of this bundle")
			}
			if sel.Quantity < 1 || sel.Quantity > 99 {
				return httperr.C(fiber.StatusBadRequest, "selection quantity (1-99) required")
			}
			vid, err := bundles.DefaultVariant(ctx, tx, sel.ProductID)
			if errors.Is(err, pgx.ErrNoRows) {
				return httperr.C(fiber.StatusConflict, "a selected product is no longer available")
			}
			if err != nil {
				return httperr.ErrInternalServerError
			}
			lines = append(lines, line{productID: sel.ProductID, variantID: vid, quantity: sel.Quantity})
		}
	default:
		return httperr.C(fiber.StatusBadRequest, "invalid bundle type")
	}

	// One cart per session; create on first use (mirrors AddItem).
	if _, err := tx.Exec(ctx, `
		INSERT INTO carts (tenant_id, customer_session)
		VALUES ($1, $2)
		ON CONFLICT (tenant_id, customer_session) DO NOTHING`, tid, session); err != nil {
		return httperr.ErrInternalServerError
	}
	var cartID string
	if err := tx.QueryRow(ctx,
		"SELECT id FROM carts WHERE tenant_id = $1 AND customer_session = $2", tid, session).Scan(&cartID); err != nil {
		return httperr.ErrInternalServerError
	}

	for _, l := range lines {
		var exists int
		if err := tx.QueryRow(ctx, `
			SELECT 1 FROM cart_items WHERE cart_id = $1 AND variant_id = $2 AND tenant_id = $3`,
			cartID, l.variantID, tid).Scan(&exists); err == nil {
			return httperr.C(fiber.StatusConflict,
				"a bundle item is already in your cart; adjust it before adding this bundle")
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return httperr.ErrInternalServerError
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO cart_items (tenant_id, cart_id, product_id, variant_id, quantity, bundle_id)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			tid, cartID, l.productID, l.variantID, l.quantity, req.BundleID); err != nil {
			return httperr.ErrInternalServerError
		}
	}

	if _, err := tx.Exec(ctx,
		"UPDATE carts SET last_activity_at = now() WHERE tenant_id = $1 AND customer_session = $2",
		tid, session); err != nil {
		return httperr.ErrInternalServerError
	}

	cp, err := loadCart(c, tx, session)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	return c.JSON(cartJSON(cp))
}

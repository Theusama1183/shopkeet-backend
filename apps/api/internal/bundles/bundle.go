package bundles

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// Errors returned by the shared helpers; handlers map them to HTTP statuses.
var (
	ErrNotFound  = errors.New("bundle not found")
	ErrNotActive = errors.New("bundle not active")
)

// BundleRow is the minimal bundle configuration needed for pricing and for
// expanding a bundle into cart lines. Exactly one of BundlePriceCents /
// DiscountPercent is set (enforced by a DB CHECK).
type BundleRow struct {
	ID               string
	Type             string
	Status           string
	BundlePriceCents *int
	DiscountPercent  *int
}

// BundleItem couples a product with its role inside a bundle: for 'fixed'
// bundles Quantity is the units each bundle contains; for 'mix_and_match' it is
// 1 and the row merely marks the product as part of the eligible pool.
type BundleItem struct {
	ProductID string
	Quantity  int
}

// LoadBundle fetches one bundle by id (RLS-scoped to the request tenant).
// Returns (nil, nil) when it does not exist.
func LoadBundle(ctx context.Context, tx pgx.Tx, bundleID string) (*BundleRow, error) {
	var b BundleRow
	err := tx.QueryRow(ctx, `
		SELECT id, type, status, bundle_price_cents, discount_percent
		FROM bundles WHERE id = $1`, bundleID).
		Scan(&b.ID, &b.Type, &b.Status, &b.BundlePriceCents, &b.DiscountPercent)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &b, nil
}

// ActiveBundle returns the bundle only when it exists and is purchasable
// (status = 'active'); a missing or non-active bundle maps to ErrNotFound /
// ErrNotActive respectively.
func ActiveBundle(ctx context.Context, tx pgx.Tx, bundleID string) (*BundleRow, error) {
	b, err := LoadBundle(ctx, tx, bundleID)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, ErrNotFound
	}
	if b.Status != "active" {
		return nil, ErrNotActive
	}
	return b, nil
}

// BundleItems returns the bundle's product -> quantity map, keyed by product id.
func BundleItems(ctx context.Context, tx pgx.Tx, bundleID string) (map[string]int, error) {
	rows, err := tx.Query(ctx,
		`SELECT product_id, quantity FROM bundle_items WHERE bundle_id = $1`, bundleID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]int{}
	for rows.Next() {
		var pid string
		var q int
		if err := rows.Scan(&pid, &q); err != nil {
			return nil, err
		}
		m[pid] = q
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

// DefaultVariant resolves the storefront variant for a bundle's product — the
// cheapest active variant of the active product. Returns pgx.ErrNoRows when
// the product cannot be sold.
func DefaultVariant(ctx context.Context, tx pgx.Tx, productID string) (string, error) {
	var id string
	err := tx.QueryRow(ctx, `
		SELECT v.id FROM product_variants v
		JOIN products p ON p.id = v.product_id
		WHERE v.product_id = $1 AND v.status = 'active' AND p.status = 'active'
		ORDER BY v.price_cents ASC, v.id ASC LIMIT 1`, productID).Scan(&id)
	return id, err
}

package bundles

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// CartItem is one cart_items row fed to the pricing pass.
type CartItem struct {
	CartItemID string
	ProductID  string
	VariantID  string
	Quantity   int
	PriceCents int
	BundleID   *string
}

type qBreak struct {
	minQty int
	pct    int
}

// PriceCart computes the amount charged for each cart line (returned keyed by
// CartItemID) and the goods subtotal. The pass honors two Phase 25 constructs:
//
//   - Bundle lines are charged as a unit: a 'fixed' bundle prices at
//     bundle_price_cents per complete set, a percentage bundle at
//     discount_percent off the summed components actually in the cart (which is
//     the only pricing form 'mix_and_match' allows), never at the raw sum of
//     the individual component prices.
//   - Plain (non-bundle) lines get the best qualifying per-product quantity
//     break: the highest discount_percent whose min_quantity their line
//     quantity clears. Quantity breaks never stack onto bundle lines — a bundle
//     already carries its own discount.
//
// A bundle referenced by the cart but deleted is priced at its component sum
// (a graceful read); checkout gates bundle 'active' status before this pass so
// an unavailable bundle is rejected before an order is formed. All queries are
// RLS-scoped by the request transaction, so no tenant id is needed here.
func PriceCart(ctx context.Context, tx pgx.Tx, items []CartItem) (map[string]int, int, error) {
	effective := make(map[string]int, len(items))
	subtotal := 0

	cfgs, err := loadBundlesByIDs(ctx, tx, bundleIDSet(items))
	if err != nil {
		return nil, 0, err
	}

	var plain []CartItem
	byBundle := map[string][]CartItem{}
	for _, it := range items {
		if it.BundleID != nil && cfgs[*it.BundleID] != nil {
			byBundle[*it.BundleID] = append(byBundle[*it.BundleID], it)
		} else {
			plain = append(plain, it)
		}
	}

	for id, group := range byBundle {
		total, perLine, err := priceBundleGroup(ctx, tx, cfgs[id], group)
		if err != nil {
			return nil, 0, err
		}
		for _, it := range group {
			effective[it.CartItemID] = perLine[it.CartItemID]
		}
		subtotal += total
	}

	breaks, err := loadQuantityBreaks(ctx, tx)
	if err != nil {
		return nil, 0, err
	}
	for _, it := range plain {
		line := it.PriceCents * it.Quantity
		if pct := bestBreak(breaks[it.ProductID], it.Quantity); pct > 0 {
			line = line * (100 - pct) / 100
		}
		effective[it.CartItemID] = line
		subtotal += line
	}
	return effective, subtotal, nil
}

func priceBundleGroup(ctx context.Context, tx pgx.Tx, b *BundleRow, group []CartItem) (int, map[string]int, error) {
	perLine := make(map[string]int, len(group))
	compSum := 0
	for _, it := range group {
		compSum += it.PriceCents * it.Quantity
	}
	if compSum == 0 {
		compSum = 1
	}

	var total int
	switch {
	case b.BundlePriceCents != nil:
		// Flat price per complete bundle. bundleQty is the smallest number of
		// complete component sets present (a shopper who shrunk one component
		// line just pays for fewer bundles); lines share the flat total
		// pro-rata by their real value so the group sums exactly.
		unit := *b.BundlePriceCents
		runs, err := minCompleteRuns(ctx, tx, b, group)
		if err != nil {
			return 0, nil, err
		}
		total = unit * max(runs, 1)
		alloc := 0
		for i, it := range group {
			share := total * (it.PriceCents * it.Quantity) / compSum
			if i == len(group)-1 {
				share = total - alloc // last line absorbs the rounding remainder
			}
			perLine[it.CartItemID] = share
			alloc += share
		}
	case b.DiscountPercent != nil:
		// Percent off the summed components that are actually in the cart (one
		// set for 'fixed', the customer's whole pick for 'mix_and_match').
		pct := *b.DiscountPercent
		total = compSum * (100 - pct) / 100
		alloc := 0
		for i, it := range group {
			line := it.PriceCents * it.Quantity * (100 - pct) / 100
			if i == len(group)-1 {
				line = total - alloc
			}
			perLine[it.CartItemID] = line
			alloc += line
		}
	default:
		// Unreachable via CHECK; defensive — charge components as-is.
		total = compSum
		for _, it := range group {
			perLine[it.CartItemID] = it.PriceCents * it.Quantity
		}
	}
	return total, perLine, nil
}

// minCompleteRuns counts how many complete bundles the group's component
// quantities form, relative to the configured per-bundle component quantities.
// Returns 1 under a query failure so pricing never blocks the read path.
func minCompleteRuns(ctx context.Context, tx pgx.Tx, b *BundleRow, group []CartItem) (int, error) {
	comps, err := loadComponentQtys(ctx, tx, b.ID)
	if err != nil {
		return 1, err
	}
	got := map[string]int{}
	for _, it := range group {
		got[it.ProductID] += it.Quantity
	}
	runs := 0
	first := true
	for pid, need := range comps {
		r := got[pid] / max(need, 1)
		if first {
			runs = r
			first = false
		} else if r < runs {
			runs = r
		}
	}
	return max(runs, 1), nil
}

func loadComponentQtys(ctx context.Context, tx pgx.Tx, bundleID string) (map[string]int, error) {
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

func loadBundlesByIDs(ctx context.Context, tx pgx.Tx, ids []string) (map[string]*BundleRow, error) {
	out := map[string]*BundleRow{}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT id, type, status, bundle_price_cents, discount_percent
		FROM bundles WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b BundleRow
		if err := rows.Scan(&b.ID, &b.Type, &b.Status, &b.BundlePriceCents, &b.DiscountPercent); err != nil {
			return nil, err
		}
		out[b.ID] = &b
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func loadQuantityBreaks(ctx context.Context, tx pgx.Tx) (map[string][]qBreak, error) {
	rows, err := tx.Query(ctx,
		`SELECT product_id, min_quantity, discount_percent FROM quantity_breaks`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string][]qBreak{}
	for rows.Next() {
		var pid string
		var b qBreak
		if err := rows.Scan(&pid, &b.minQty, &b.pct); err != nil {
			return nil, err
		}
		m[pid] = append(m[pid], b)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return m, nil
}

func bestBreak(breaks []qBreak, quantity int) int {
	best := 0
	for _, b := range breaks {
		if quantity >= b.minQty && b.pct > best {
			best = b.pct
		}
	}
	return best
}

func bundleIDSet(items []CartItem) []string {
	set := map[string]struct{}{}
	var ids []string
	for _, it := range items {
		if it.BundleID != nil {
			if _, ok := set[*it.BundleID]; !ok {
				set[*it.BundleID] = struct{}{}
				ids = append(ids, *it.BundleID)
			}
		}
	}
	return ids
}

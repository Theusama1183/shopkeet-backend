// Phase 32 — data-driven product recommendations.
//
// product_recommendations already carries type='manual' (Phase 26) and
// type='auto' (the schema CHECK accepts both). This file is the scheduled side
// of 'auto': a weekly job recomputes, for every product, which other products
// are most often bought in the same order (co-occurrence over order_items) and
// replaces the tenant's type='auto' rows with the top matches.
//
// Manual curation stays the winner: a pair with a manual recommendation is
// never auto-generated, and the public/admin list hides a stale auto row when a
// manual row exists for the same pair, so a merchant override always wins.
package recommendations

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// autoLimit caps how many auto picks a product may carry in one pass.
	autoLimit = 5

	// autoMinOrders is the "enough order history" threshold: a product pair
	// must appear together in at least this many distinct (non-cancelled)
	// orders for an auto recommendation to be created. Pairs that co-occur
	// once are noise; two orders is a signal worth surfacing.
	autoMinOrders = 2
)

// manualPriority hides auto rows behind an existing manual pick for the same
// (product_id, recommended_product_id). Deployed in the list query so a manual
// override wins even when it was created after the last auto recompute.
const manualPriority = `
	AND (r.type = 'manual' OR NOT EXISTS (
		SELECT 1 FROM product_recommendations m
		WHERE m.tenant_id = r.tenant_id
		  AND m.product_id = r.product_id
		  AND m.recommended_product_id = r.recommended_product_id
		  AND m.type = 'manual'))`

// RecomputeAuto rebuilds the tenant's type='auto' recommendations from order
// co-occurrence. It must run inside an RLS-scoped transaction (the scheduled
// job opens one per tenant with app.current_tenant set). Existing auto rows for
// the tenant are replaced, never appended, so a product that stops co-purchasing
// loses its computed pick on the next run. Returns the number of auto rows
// inserted.
//
// The alternative to a CTE here would be fetching every pair into Go and
// filtering there; doing it in SQL keeps the recompute a single pass and lets
// the top-N + status + manual-priority filters live next to the query.
func RecomputeAuto(ctx context.Context, tx pgx.Tx, tenantID string) (int, error) {
	if _, err := tx.Exec(ctx,
		"DELETE FROM product_recommendations WHERE tenant_id = $1 AND type = 'auto'",
		tenantID); err != nil {
		return 0, err
	}

	tag, err := tx.Exec(ctx, `
		WITH pairs AS (
			SELECT DISTINCT a.order_id AS order_id, a.product_id AS a, b.product_id AS b
			FROM order_items a
			JOIN order_items b ON b.order_id = a.order_id AND b.product_id <> a.product_id
			JOIN orders o ON o.id = a.order_id AND o.status <> 'cancelled'
			WHERE a.tenant_id = $3 AND b.tenant_id = $3 AND o.tenant_id = $3
		),
		counts AS (
			SELECT a, b, count(*) AS cnt
			FROM pairs
			GROUP BY a, b
		),
		ranked AS (
			SELECT a, b, row_number() OVER (PARTITION BY a ORDER BY cnt DESC, b) AS rn
			FROM counts
			WHERE cnt >= $1
		),
		eligible AS (
			SELECT r.a, r.b, r.rn
			FROM ranked r
			WHERE r.rn <= $2
			  AND EXISTS (SELECT 1 FROM products pa WHERE pa.id = r.a AND pa.tenant_id = $3 AND pa.status = 'active')
			  AND EXISTS (SELECT 1 FROM products pb WHERE pb.id = r.b AND pb.tenant_id = $3 AND pb.status = 'active')
			  AND NOT EXISTS (
				  SELECT 1 FROM product_recommendations m
				  WHERE m.tenant_id = $3 AND m.product_id = r.a
					AND m.recommended_product_id = r.b AND m.type = 'manual')
		)
		INSERT INTO product_recommendations (tenant_id, product_id, recommended_product_id, type, sort_order)
		SELECT $3, a, b, 'auto', rn - 1 FROM eligible`,
		autoMinOrders, autoLimit, tenantID)
	if err != nil {
		return 0, err
	}
	return int(tag.RowsAffected()), nil
}

// RecomputeAllTenants drives the weekly job: every tenant gets its auto rows
// recomputed in its own RLS-scoped transaction (the same pattern the
// abandoned-cart sweep uses — a scheduled job must set app.current_tenant
// itself or FORCE RLS returns zero rows). Returns the total auto rows created.
func RecomputeAllTenants(ctx context.Context, pool *pgxpool.Pool) (int, error) {
	tenantRows, err := pool.Query(ctx, "SELECT id FROM tenants ORDER BY id")
	if err != nil {
		return 0, err
	}
	var tenantIDs []string
	for tenantRows.Next() {
		var id string
		if err := tenantRows.Scan(&id); err != nil {
			tenantRows.Close()
			return 0, err
		}
		tenantIDs = append(tenantIDs, id)
	}
	tenantRows.Close()
	if err := tenantRows.Err(); err != nil {
		return 0, err
	}

	inserted := 0
	for _, tid := range tenantIDs {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return inserted, err
		}
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			tx.Rollback(ctx)
			return inserted, err
		}
		n, err := RecomputeAuto(ctx, tx, tid)
		if err != nil {
			tx.Rollback(ctx)
			return inserted, err
		}
		if err := tx.Commit(ctx); err != nil {
			return inserted, err
		}
		inserted += n
	}
	return inserted, nil
}
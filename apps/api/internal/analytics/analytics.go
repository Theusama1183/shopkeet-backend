// Package analytics implements the Phase 24 merchant dashboard surface — plain
// SQL aggregation over orders / order_items / carts, no new tables. "Sales"
// (the revenue metric) means any order whose status isn't cancelled;
// payment_status is deliberately ignored because COD orders are revenue at
// placement. Everything runs inside the tenant tx TenantMW opened (RLS scopes
// the aggregates), and the queries are covered by the migration-0026 indexes.
package analytics

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

const (
	periodsAll   = "7d,30d,90d"
	defaultDays  = 30
	dayLayout    = "2006-01-02"
	currencyCode = "usd" // tenants are single-currency; orders default to usd
)

// Service groups the analytics handlers behind one constructed value.
type Service struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) *Service {
	return &Service{pool: pool}
}

// parsePeriod validates ?period= against the allowed set and returns the number
// of days the window should cover. Missing → 30d. Anything else → 400.
func parsePeriod(c *fiber.Ctx) (int, error) {
	raw := strings.TrimSpace(c.Query("period", ""))
	if raw == "" {
		return defaultDays, nil
	}
	if raw[len(raw)-1] != 'd' {
		return 0, httperr.BadRequest("invalid_period", "period must be one of "+periodsAll)
	}
	days, err := strconv.Atoi(raw[:len(raw)-1])
	if err != nil {
		return 0, httperr.BadRequest("invalid_period", "period must be one of "+periodsAll)
	}
	for _, p := range strings.Split(periodsAll, ",") {
		want, _ := strconv.Atoi(strings.TrimSuffix(p, "d"))
		if want == days {
			return days, nil
		}
	}
	return 0, httperr.BadRequest("invalid_period", "period must be one of "+periodsAll)
}

// GetSales handles GET /analytics/sales: revenue and order count, bucketed per
// UTC day, plus a period total.
func (s *Service) GetSales(c *fiber.Ctx) error {
	days, err := parsePeriod(c)
	if err != nil {
		return err
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	rows, err := tx.Query(c.Context(), `
		SELECT date_trunc('day', created_at)::date,
		       COALESCE(SUM(total_cents), 0)::int,
		       COUNT(*)::int
		FROM orders
		WHERE tenant_id = $1
		  AND created_at >= now() - make_interval(days => $2)
		  AND status <> 'cancelled'
		GROUP BY date_trunc('day', created_at)
		ORDER BY date_trunc('day', created_at)`, tenantID(c), days)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	var buckets []fiber.Map
	totRev, totCount := 0, 0
	for rows.Next() {
		var b salesBucket
		if err := rows.Scan(&b.date, &b.revenueCents, &b.orderCount); err != nil {
			return httperr.ErrInternalServerError
		}
		totRev += b.revenueCents
		totCount += b.orderCount
		buckets = append(buckets, fiber.Map{
			"date": b.date, "revenue_cents": b.revenueCents, "order_count": b.orderCount,
		})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	if buckets == nil {
		buckets = []fiber.Map{}
	}
	return c.JSON(fiber.Map{
		"period":   fmt.Sprintf("%dd", days),
		"currency": currencyCode,
		"totals":   fiber.Map{"revenue_cents": totRev, "order_count": totCount},
		"buckets":  buckets,
	})
}

type salesBucket struct {
	date         string
	revenueCents int
	orderCount   int
}

// GetTopProducts handles GET /analytics/top-products: best sellers grouped by
// product (a variant sale rolls up to its product), ranked by ?metric
// (quantity | revenue, default quantity). Groups are filtered the same way the
// sales metric is — cancelled orders contribute nothing.
func (s *Service) GetTopProducts(c *fiber.Ctx) error {
	days, err := parsePeriod(c)
	if err != nil {
		return err
	}
	metric := strings.ToLower(strings.TrimSpace(c.Query("metric", "quantity")))
	if metric != "quantity" && metric != "revenue" {
		return httperr.BadRequest("invalid_metric", "metric must be 'quantity' or 'revenue'")
	}
	limit, err := parseLimit(c.Query("limit", "10"), 50)
	if err != nil {
		return err
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	orderBy := "quantity DESC"
	if metric == "revenue" {
		orderBy = "revenue_cents DESC"
	}
	rows, err := tx.Query(c.Context(), `
		SELECT p.id, p.name,
		       SUM(oi.quantity)::int,
		       SUM(oi.quantity * oi.unit_price_cents)::int
		FROM order_items oi
		JOIN orders o ON o.id = oi.order_id
		JOIN products p ON p.id = oi.product_id
		WHERE oi.tenant_id = $1
		  AND o.created_at >= now() - make_interval(days => $2)
		  AND o.status <> 'cancelled'
		GROUP BY p.id, p.name
		ORDER BY `+orderBy+` LIMIT $3`, tenantID(c), days, limit)
	if err != nil {
		return httperr.ErrInternalServerError
	}
	defer rows.Close()

	var items []fiber.Map
	for rows.Next() {
		var tp topProduct
		if err := rows.Scan(&tp.productID, &tp.productName, &tp.quantity, &tp.revenueCents); err != nil {
			return httperr.ErrInternalServerError
		}
		items = append(items, fiber.Map{
			"product_id": tp.productID, "product_name": tp.productName,
			"quantity": tp.quantity, "revenue_cents": tp.revenueCents,
		})
	}
	if err := rows.Err(); err != nil {
		return httperr.ErrInternalServerError
	}
	if items == nil {
		items = []fiber.Map{}
	}
	return c.JSON(fiber.Map{
		"period": fmt.Sprintf("%dd", days), "metric": metric, "currency": currencyCode,
		"items": items,
	})
}

type topProduct struct {
	productID    string
	productName  string
	quantity     int
	revenueCents int
}

// GetConversion handles GET /analytics/conversion: carts created vs orders
// placed in the window — a deliberately coarse funnel (one row per cart) so the
// storefront has a "started vs. bought" number to design against.
func (s *Service) GetConversion(c *fiber.Ctx) error {
	days, err := parsePeriod(c)
	if err != nil {
		return err
	}
	tx, ok := txFrom(c)
	if !ok {
		return httperr.ErrInternalServerError
	}
	var cartsCreated, ordersPlaced int
	if err := tx.QueryRow(c.Context(), `
		SELECT (SELECT COUNT(*)::int FROM carts
		         WHERE tenant_id = $1 AND created_at >= now() - make_interval(days => $2)),
		       (SELECT COUNT(*)::int FROM orders
		         WHERE tenant_id = $1 AND created_at >= now() - make_interval(days => $2))`,
		tenantID(c), days).Scan(&cartsCreated, &ordersPlaced); err != nil {
		return httperr.ErrInternalServerError
	}
	rate := 0.0
	if cartsCreated > 0 {
		rate = round4(float64(ordersPlaced) / float64(cartsCreated))
	}
	return c.JSON(fiber.Map{
		"period":          fmt.Sprintf("%dd", days),
		"carts_created":   cartsCreated,
		"orders_placed":   ordersPlaced,
		"conversion_rate": rate,
	})
}

func parseLimit(raw string, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 10, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 || n > max {
		return 0, httperr.BadRequest("invalid_limit", "limit must be an integer 1-"+strconv.Itoa(max))
	}
	return n, nil
}

func round4(f float64) float64 {
	return float64(int64(f*10000+0.5)) / 10000
}

// --- helpers --------------------------------------------------------------------

func txFrom(c *fiber.Ctx) (pgx.Tx, bool) {
	tx, ok := c.Locals("tx").(pgx.Tx)
	return tx, ok && tx != nil
}

func tenantID(c *fiber.Ctx) string {
	id, _ := c.Locals("tenant_id").(string)
	return id
}

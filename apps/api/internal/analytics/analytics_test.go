package analytics

import (
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/valyala/fasthttp"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// TestParsePeriodAndLimit is the pure half of Phase 24 validation: period
// defaults to 30d, accepts exactly 7d/30d/90d, and limits stay within 1..50.
func TestParsePeriodAndLimit(t *testing.T) {
	app := fiber.New()
	c := app.AcquireCtx(&fasthttp.RequestCtx{})
	defer app.ReleaseCtx(c)

	c.Request().SetRequestURI("/analytics/sales")
	if d, _ := parsePeriod(c); d != 30 {
		t.Fatalf("missing period should default to 30d, got %d", d)
	}
	c.Request().SetRequestURI("/analytics/sales?period=7d")
	if d, _ := parsePeriod(c); d != 7 {
		t.Fatalf("7d parsed as %d", d)
	}
	c.Request().SetRequestURI("/analytics/sales?period=90d")
	if d, _ := parsePeriod(c); d != 90 {
		t.Fatalf("90d parsed as %d", d)
	}
	for _, bad := range []string{"1d", "45d", "365d", "abc", "30D", "30"} {
		c.Request().SetRequestURI("/analytics/sales?period=" + bad)
		if _, err := parsePeriod(c); err == nil {
			t.Fatalf("period %q should be rejected", bad)
		}
	}
	if _, err := parseLimit("0", 50); err == nil {
		t.Fatal("limit 0 should be rejected")
	}
	if _, err := parseLimit("51", 50); err == nil {
		t.Fatal("limit 51 should be rejected")
	}
	if n, _ := parseLimit("", 50); n != 10 {
		t.Fatalf("default limit should be 10, got %d", n)
	}
}

type anResp struct {
	Period   string `json:"period"`
	Currency string `json:"currency"`
	Metric   string `json:"metric"`
	Totals   struct {
		RevenueCents int `json:"revenue_cents"`
		OrderCount   int `json:"order_count"`
	} `json:"totals"`
	Buckets []struct {
		Date         string `json:"date"`
		RevenueCents int    `json:"revenue_cents"`
		OrderCount   int    `json:"order_count"`
	} `json:"buckets"`
	Items []struct {
		ProductID    string `json:"product_id"`
		ProductName  string `json:"product_name"`
		Quantity     int    `json:"quantity"`
		RevenueCents int    `json:"revenue_cents"`
	} `json:"items"`
	CartsCreated   int     `json:"carts_created"`
	OrdersPlaced   int     `json:"orders_placed"`
	ConversionRate float64 `json:"conversion_rate"`
}

// TestAnalyticsReconciliation carries the Phase 24 acceptance:
//   - sales-over-time totals reconcile EXACTLY against a manual
//     SUM(total_cents) over the same period (and differ meaningfully from a sum
//     that would wrongly include a cancelled order);
//   - top-products ranks by the requested metric (quantity vs. revenue) — the
//     winner is not always the same product;
//   - the window is enforced (a 40-day-old order is invisible).
func TestAnalyticsReconciliation(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	const secret = "test-secret"
	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"analytics", "an-"+sfx()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	adminToken, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer seedTx.Rollback(ctx)
	if _, err := seedTx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	seedProduct := func(name string, price int) (prodID, variantID string) {
		if err := seedTx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'usd', 100, 'active') RETURNING id`,
			tid, name, name+"-"+sfx(), price).Scan(&prodID); err != nil {
			t.Fatalf("seed product %s: %v", name, err)
		}
		if err := seedTx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, 100, 'active') RETURNING id`,
			tid, prodID, price).Scan(&variantID); err != nil {
			t.Fatalf("seed variant %s: %v", name, err)
		}
		return prodID, variantID
	}

	// Cart A (converted) + B (abandoned) + C + D: funnel = 4 carts / 2 real sales.
	now := time.Now()
	for i := 0; i < 4; i++ {
		if _, err := seedTx.Exec(ctx, `
			INSERT INTO carts (tenant_id, customer_session, created_at)
			VALUES ($1, $2, $3)`, tid, "sess-an-"+sfx(), now); err != nil {
			t.Fatalf("seed cart: %v", err)
		}
	}

	productA, vA := seedProduct("Teapot", 100)
	productB, vB := seedProduct("Mug", 500)

	placeOrder := func(customer, status string, total int, at time.Time, items ...[3]any) {
		var orderID string
		if err := seedTx.QueryRow(ctx, `
			INSERT INTO orders (tenant_id, customer_name, customer_phone, status,
			                    total_cents, currency, shipping_cost_cents, created_at)
			VALUES ($1, $2, $3, $4, $5, 'usd', 0, $6) RETURNING id`,
			tid, customer, "+1-555-0000", status, total, at).Scan(&orderID); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		for _, it := range items {
			prodID := it[0].(string)
			varID := it[1].(string)
			qty := it[2].(int)
			var unit int
			if err := seedTx.QueryRow(ctx,
				"SELECT price_cents FROM product_variants WHERE id = $1", varID).Scan(&unit); err != nil {
				t.Fatalf("read variant price: %v", err)
			}
			if _, err := seedTx.Exec(ctx, `
				INSERT INTO order_items (tenant_id, order_id, product_id, variant_id, quantity, unit_price_cents)
				VALUES ($1, $2, $3, $4, $5, $6)`,
				tid, orderID, prodID, varID, qty, unit); err != nil {
				t.Fatalf("seed order_item: %v", err)
			}
		}
	}

	// Order 1 today: 3× Teapot (300). Order 2 today: 2× Mug (1000).
	// Order 3 today: 1× Teapot (100) but CANCELLED — must not count as revenue
	// or top-product sales, though it still counts as an order placed in the
	// funnel. Order 4: 5× Teapot (500) 40 days ago — outside every window.
	placeOrder("ada", "confirmed", 300, now, [3]any{productA, vA, 3})
	placeOrder("bob", "delivered", 1000, now, [3]any{productB, vB, 2})
	placeOrder("cancelled-guest", "cancelled", 100, now, [3]any{productA, vA, 1})
	placeOrder("old-guest", "delivered", 500, now.Add(-40*24*time.Hour), [3]any{productA, vA, 5})
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Manual reconciliation over the same predicate (non-cancelled, in-window).
	var manualRev, manualCount, manualWithCancelled int
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(total_cents), 0), COUNT(*)
		FROM orders WHERE tenant_id = $1
		  AND created_at >= now() - make_interval(days => 30)
		  AND status <> 'cancelled'`, tid).Scan(&manualRev, &manualCount); err != nil {
		t.Fatalf("manual sales sum: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(total_cents), 0)
		FROM orders WHERE tenant_id = $1
		  AND created_at >= now() - make_interval(days => 30)`, tid).Scan(&manualWithCancelled); err != nil {
		t.Fatalf("manual incl cancelled: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, secret, New(pool))

	do := func(path string, want int) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("X-Tenant-ID", tid)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("GET %s: status %d, want %d (body=%s)", path, res.StatusCode, want, raw)
		}
		return string(raw)
	}

	var sales anResp
	if err := json.Unmarshal([]byte(do("/analytics/sales?period=30d", fiber.StatusOK)), &sales); err != nil {
		t.Fatalf("decode sales: %v", err)
	}
	if sales.Totals.RevenueCents != manualRev || sales.Totals.RevenueCents != 1300 ||
		sales.Totals.OrderCount != manualCount || sales.Totals.OrderCount != 2 {
		t.Fatalf("sales totals do not reconcile: api=%+v manual=(%d,%d) incl-cancelled=%d",
			sales.Totals, manualRev, manualCount, manualWithCancelled)
	}
	if len(sales.Buckets) != 2 { // today is the only populated day
		t.Fatalf("expected a single populated day bucket, got %+v", sales.Buckets)
	}
	if sales.Currency != "usd" {
		t.Fatalf("currency wrong: %q", sales.Currency)
	}
	if err := json.Unmarshal([]byte(do("/analytics/sales?period=90d", fiber.StatusOK)), &sales); err != nil {
		t.Fatalf("decode 90d: %v", err)
	}
	if sales.Totals.RevenueCents != 1300 {
		t.Fatalf("90d window should equal 30d (old order excluded): %+v", sales.Totals)
	}

	// Reconcile EVERY bucket against the manual day aggregate, not just totals.
	type dayTot struct {
		Rev int
		Cnt int
	}
	manualDays := map[string]dayTot{}
	rows, err := pool.Query(ctx, `
		SELECT date_trunc('day', created_at)::date, COALESCE(SUM(total_cents),0), COUNT(*)
		FROM orders WHERE tenant_id = $1
		  AND created_at >= now() - make_interval(days => 30)
		  AND status <> 'cancelled'
		GROUP BY date_trunc('day', created_at)`, tid)
	if err != nil {
		t.Fatalf("manual day buckets: %v", err)
	}
	for rows.Next() {
		var d string
		var dt dayTot
		if err := rows.Scan(&d, &dt.Rev, &dt.Cnt); err != nil {
			t.Fatalf("scan day: %v", err)
		}
		manualDays[d] = dt
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatalf("day rows: %v", err)
	}
	if len(sales.Buckets) != len(manualDays) {
		t.Fatalf("bucket count mismatch: api=%d manual=%d", len(sales.Buckets), len(manualDays))
	}
	for _, b := range sales.Buckets {
		m, ok := manualDays[b.Date]
		if !ok || m.Rev != b.RevenueCents || m.Cnt != b.OrderCount {
			t.Fatalf("bucket %s mismatch: api=(%d,%d) manual=(%d,%d)",
				b.Date, b.RevenueCents, b.OrderCount, m.Rev, m.Cnt)
		}
	}

	// Top products: by quantity the cheap Teapot wins (3>2); by revenue the
	// expensive Mug wins (1000>300). Cancelled/old orders contribute nothing.
	raw := do("/analytics/top-products?period=30d&metric=quantity", fiber.StatusOK)
	var tp anResp
	if err := json.Unmarshal([]byte(raw), &tp); err != nil {
		t.Fatalf("decode top-products quantity: %v", err)
	}
	if tp.Items[0].ProductName != "Teapot" || tp.Items[0].Quantity != 3 || tp.Items[0].RevenueCents != 300 {
		t.Fatalf("quantity ranking wrong: %+v", tp.Items)
	}
	if tp.Items[1].ProductName != "Mug" || tp.Items[1].Quantity != 2 {
		t.Fatalf("second item wrong: %+v", tp.Items)
	}
	if tp.Metric != "quantity" {
		t.Fatalf("metric echo wrong: %q", tp.Metric)
	}

	if err := json.Unmarshal([]byte(do("/analytics/top-products?period=30d&metric=revenue", fiber.StatusOK)), &tp); err != nil {
		t.Fatalf("decode top-products revenue: %v", err)
	}
	if tp.Items[0].ProductName != "Mug" || tp.Items[0].RevenueCents != 1000 {
		t.Fatalf("revenue ranking wrong (Mug must rank first): %+v", tp.Items)
	}
	if tp.Items[1].ProductName != "Teapot" || tp.Items[1].RevenueCents != 300 {
		t.Fatalf("revenue second wrong: %+v", tp.Items)
	}

	// Funnel: 4 carts created → 3 orders placed (cancelled still placed) with
	// 2 real paid-through sales. Rate = 3/4 = 0.75.
	if err := json.Unmarshal([]byte(do("/analytics/conversion?period=30d", fiber.StatusOK)), &tp); err != nil {
		t.Fatalf("decode conversion: %v", err)
	}
	if tp.CartsCreated != 4 || tp.OrdersPlaced != 3 {
		t.Fatalf("funnel counts wrong: carts=%d orders=%d", tp.CartsCreated, tp.OrdersPlaced)
	}
	if tp.ConversionRate != 0.75 {
		t.Fatalf("conversion_rate wrong: %v", tp.ConversionRate)
	}

	// Validation errors.
	for _, bad := range []string{
		"/analytics/sales?period=45d",
		"/analytics/top-products?period=30d&metric=units",
		"/analytics/top-products?period=30d&limit=0",
		"/analytics/top-products?period=30d&limit=51",
	} {
		do(bad, fiber.StatusBadRequest)
	}

	// RLS: tenant B's admin sees an empty dashboard (no cross-tenant bleed).
	var tidB string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"analytics-b", "an-b-"+sfx()).Scan(&tidB); err != nil {
		t.Fatalf("seed tenant B: %v", err)
	}
	tokB, _ := auth.Sign(secret, tidB, tidB[:8], "owner", time.Hour)
	req := httptest.NewRequest("GET", "/api/v1/analytics/sales?period=30d", nil)
	req.Header.Set("Authorization", "Bearer "+tokB)
	req.Header.Set("X-Tenant-ID", tidB)
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("tenant B sales: %v", err)
	}
	rawB, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var tb anResp
	if err := json.Unmarshal(rawB, &tb); err != nil {
		t.Fatalf("decode tenant B sales: %v", err)
	}
	if tb.Totals.RevenueCents != 0 || tb.Totals.OrderCount != 0 || len(tb.Buckets) != 0 {
		t.Fatalf("tenant B saw tenant A's data: %+v", tb.Totals)
	}
}

func sfx() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
}

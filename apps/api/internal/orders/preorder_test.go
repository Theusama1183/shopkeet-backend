package orders

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// TestPreorderCheckout is the Phase 19 acceptance criterion: checkout succeeds
// against zero stock for an allow_preorder variant, the line is marked
// is_preorder=true, and inventory_count is untouched. Non-preorderable
// zero-stock variants are still refused with 409. A partially-stocked
// preorderable variant becomes a preorder line only when quantity exceeds the
// on-hand inventory; within stock it is a normal sale that decrements.
func TestPreorderCheckout(t *testing.T) {
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
	sfx := randSuffix5()
	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"preorder-"+sfx, "pr-"+sfx).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	seedSV := func(slug string, inv int, allowPreorder bool) (pid, vid string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'usd', $5, 'active') RETURNING id`,
			tid, slug, "pre-"+slug+"-"+sfx, 1000, inv).Scan(&pid); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status, allow_preorder)
			VALUES ($1, $2, $3, $4, 'active', $5) RETURNING id`,
			tid, pid, 1000, inv, allowPreorder).Scan(&vid); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return pid, vid
	}
	_, vp := seedSV("preorder", 0, true)     // zero stock, preorderable
	_, vn := seedSV("normal", 0, false)      // zero stock, not preorderable
	_, vh := seedSV("hybrid", 1, true)       // 1 unit, preorderable

	var zoneID, rateID string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin zone: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_zones (tenant_id, name, countries, regions)
		VALUES ($1, 'PK', $2, '{}') RETURNING id`, tid, `{"PK"}`).Scan(&zoneID); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order)
		VALUES ($1, $2, 'Standard', 500, NULL, 0) RETURNING id`, tid, zoneID).Scan(&rateID); err != nil {
		t.Fatalf("seed rate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit zone: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, secret, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	bus := events.NewBus()
	RegisterRoutes(v1, pool, secret, New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))

	do := func(method, path, session, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", tid)
		if session != "" {
			req.Header.Set("X-Customer-Session", session)
		}
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("%s %s: status %d, want %d (body=%s)", method, path, res.StatusCode, want, raw)
		}
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
		return res
	}
	checkout := func(session string) *http.Response {
		return do("POST", "/api/v1/checkout", session,
			`{"customer_name":"Pre","customer_phone":"+1-555-`+sfx+`","customer_email":"pre@example.com",`+
				`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
				`"shipping_rate_id":"`+rateID+`"}`, fiber.StatusCreated)
	}
	var itemPreorder = func(vid string) (bool, int) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin read: %v", err)
		}
		defer tx.Rollback(ctx)
		_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid)
		var isPre bool
		// order_items.id is a random UUID, so "newest" must come from the
		// order's monotonic created_at, not the id column.
		if err := tx.QueryRow(ctx, `
			SELECT oi.is_preorder FROM order_items oi
			JOIN orders o ON o.id = oi.order_id
			WHERE oi.variant_id = $1
			ORDER BY o.created_at DESC LIMIT 1`, vid).
			Scan(&isPre); err != nil {
			t.Fatalf("read order item: %v", err)
		}
		var inv int
		if err := tx.QueryRow(ctx,
			"SELECT inventory_count FROM product_variants WHERE id = $1", vid).
			Scan(&inv); err != nil {
			t.Fatalf("read inventory: %v", err)
		}
		return isPre, inv
	}

	// Zero-stock preorderable variant: 2 units sell, marked is_preorder, no
	// decrement.
	do("POST", "/api/v1/cart", "s-p-"+sfx, `{"variant_id":"`+vp+`","quantity":2}`, fiber.StatusOK)
	checkout("s-p-" + sfx)
	if isPre, inv := itemPreorder(vp); !isPre || inv != 0 {
		t.Fatalf("preorder line: is_preorder=%v inventory=%d, want true/0", isPre, inv)
	}

	// Non-preorderable zero-stock variant: refused at checkout.
	do("POST", "/api/v1/cart", "s-n-"+sfx, `{"variant_id":"`+vn+`","quantity":1}`, fiber.StatusOK)
	do("POST", "/api/v1/checkout", "s-n-"+sfx,
		`{"customer_name":"Pre","customer_phone":"+1-555-`+sfx+`","shipping_address_line1":"1 Main St",`+
			`"shipping_city":"Lahore","shipping_country":"PK","shipping_rate_id":"`+rateID+`"}`,
		fiber.StatusConflict)

	// Partially stocked preorderable variant sold ABOVE stock: whole line is a
	// preorder, inventory untouched.
	do("POST", "/api/v1/cart", "s-h-"+sfx, `{"variant_id":"`+vh+`","quantity":3}`, fiber.StatusOK)
	checkout("s-h-" + sfx)
	if isPre, inv := itemPreorder(vh); !isPre || inv != 1 {
		t.Fatalf("oversold hybrid line: is_preorder=%v inventory=%d, want true/1", isPre, inv)
	}

	// Same variant within stock: a normal sale that decrements once.
	do("POST", "/api/v1/cart", "s-h2-"+sfx, `{"variant_id":"`+vh+`","quantity":1}`, fiber.StatusOK)
	checkout("s-h2-" + sfx)
	if isPre, inv := itemPreorder(vh); isPre || inv != 0 {
		t.Fatalf("in-stock hybrid line: is_preorder=%v inventory=%d, want false/0", isPre, inv)
	}

	// The non-preorderable variant was never sold (no order_items rows for it).
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin check: %v", err)
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid)
	var n int
	_ = tx.QueryRow(ctx, "SELECT count(*) FROM order_items WHERE variant_id = $1", vn).Scan(&n)
	if n != 0 {
		t.Fatalf("non-preorderable zero-stock variant got %d order_items, want 0", n)
	}
	_ = tx.Commit(ctx)
}
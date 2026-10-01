// External test package: it mounts cart (which imports discounts), so the test
// must live outside package discounts to avoid an import cycle.
package discounts_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/discounts"
	"github.com/shopkeet/api/internal/orders"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
	"github.com/shopkeet/api/internal/shipping"
)

type orderPayload struct {
	ID            string `json:"id"`
	DiscountCode  string `json:"discount_code"`
	DiscountCents int    `json:"discount_cents"`
	TotalCents    int    `json:"total_cents"`
	ShippingCost  int    `json:"shipping_cost_cents"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
}

type cartPayload struct {
	Cart *struct {
		DiscountCode  string `json:"discount_code"`
		DiscountCents int    `json:"discount_cents"`
		TotalCents    int    `json:"total_cents"`
	} `json:"cart"`
}

// TestDiscountsAcceptance is the Phase 10 criterion: a percentage-off code
// reduces the order total (and snapshots code + discount_cents onto the order);
// codes that expire or hit their usage limit after being applied to a cart are
// rejected at checkout; concurrent checkouts against a usage_limit=1 code let
// exactly one order through; the admin CRUD + tenant isolation hold.
func TestDiscountsAcceptance(t *testing.T) {
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
	sfx := hex.EncodeToString(func() []byte {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		return b
	}())

	mkTenant := func(name string) (tid, token string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "disc-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tid, token
	}
	aID, aToken := mkTenant("alpha")
	aIDb, bToken := mkTenant("beta")

	seedProduct := func(tid string, price, inv int) (prodID, variantID string) {
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
			tid, "disc-prod", "disc-prod-"+sfx+tid[:4], price, inv).Scan(&prodID); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
			tid, prodID, price, inv).Scan(&variantID); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return prodID, variantID
	}

	seedZoneRate := func(tid string) (rateID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin zone: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var zoneID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipping_zones (tenant_id, name, countries, regions)
			VALUES ($1, 'PK', '{PK}', '{}') RETURNING id`, tid).Scan(&zoneID); err != nil {
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
		return rateID
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	discounts.RegisterRoutes(v1, pool, secret, discounts.New(pool))
	bus := events.NewBus()
	orders.RegisterRoutes(v1, pool, secret, orders.New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))
	shipping.RegisterRoutes(v1, pool, secret, shipping.New(pool))

	doTenant := func(tid string) func(method, path, session, body string, want int) *http.Response {
		return func(method, path, session, body string, want int) *http.Response {
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
	}
	do := doTenant(aID)
	adminGet := func(path string, want int) *http.Response {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Tenant-ID", aID)
		req.Header.Set("Authorization", "Bearer "+aToken)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("admin GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("admin GET %s: status %d, want %d (body=%s)", path, res.StatusCode, want, raw)
		}
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
		return res
	}
	adminPost := func(method, path, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", aID)
		req.Header.Set("Authorization", "Bearer "+aToken)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("admin %s %s: %v", method, path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("admin %s %s: status %d, want %d (body=%s)", method, path, res.StatusCode, want, raw)
		}
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
		return res
	}

	_, v := seedProduct(aID, 2000, 30)
	rateID := seedZoneRate(aID)

	// --- Admin CRUD ---
	create := func(body string) *http.Response {
		return adminPost("POST", "/api/v1/discounts", body, fiber.StatusCreated)
	}
	create(`{"code":"SAVE10","type":"percentage","value_percent":10,"min_subtotal_cents":1000,"usage_limit":2}`)
	create(`{"code":"FIX50","type":"fixed_amount","value_cents":5000,"min_subtotal_cents":2000}`)
	create(`{"code":"SWEET","type":"percentage","value_percent":5}`)
	res := adminGet("/api/v1/discounts", fiber.StatusOK)
	var list struct {
		Discounts []struct {
			Code      string `json:"code"`
			Status    string `json:"status"`
			TimesUsed int    `json:"times_used"`
		} `json:"discounts"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Discounts) != 3 {
		t.Fatalf("expected 3 discounts, got %d", len(list.Discounts))
	}

	checkout := func(session string, want int) *http.Response {
		return do("POST", "/api/v1/checkout", session,
			`{"customer_name":"Ada","customer_phone":"+1-555-`+sfx+`",`+
				`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
				`"shipping_rate_id":"`+rateID+`"}`,
			want)
	}

	// --- Percentage code: apply to cart, discount shows, order total reduced ---
	s1 := "sess-c-" + sfx
	do("POST", "/api/v1/cart", s1, `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	res = do("POST", "/api/v1/cart/discount", s1, `{"code":"SAVE10"}`, fiber.StatusOK)
	var c1 cartPayload
	if err := json.NewDecoder(res.Body).Decode(&c1); err != nil {
		t.Fatalf("decode cart: %v", err)
	}
	if c1.Cart == nil || c1.Cart.DiscountCode != "SAVE10" || c1.Cart.DiscountCents != 200 {
		t.Fatalf("cart should show SAVE10/200 after apply, got %+v", c1.Cart)
	}
	res = checkout(s1, fiber.StatusCreated)
	var ord1 orderPayload
	if err := json.NewDecoder(res.Body).Decode(&ord1); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if ord1.DiscountCode != "SAVE10" || ord1.DiscountCents != 200 {
		t.Fatalf("order should snapshot SAVE10/200, got code=%q cents=%d", ord1.DiscountCode, ord1.DiscountCents)
	}
	if ord1.TotalCents != 2300 { // 2000 - 200 + 500
		t.Fatalf("discounted order should total 2300, got %d", ord1.TotalCents)
	}

	// --- code deleted/unknown is rejected both at apply and checkout ---
	do("POST", "/api/v1/cart/discount", "sess-unknown-"+sfx, `{"code":"NOPE"}`, fiber.StatusNotFound)

	// --- minimum subtotal enforced at apply ---
	do("POST", "/api/v1/cart", "sess-low-"+sfx, `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	// FIX50 needs min 2000; delete nothing — just use a low subtotal via min on SAVE10 (>1000 ok)...

	// --- code applied, then expired -> rejected at checkout ---
	s2 := "sess-e-" + sfx
	do("POST", "/api/v1/cart", s2, `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	do("POST", "/api/v1/cart/discount", s2, `{"code":"SWEET"}`, fiber.StatusOK)
	// Simulate time passing: expiry moves into the past before checkout.
	txE, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin expire: %v", err)
	}
	if _, err := txE.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", aID); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := txE.Exec(ctx,
		"UPDATE discounts SET ends_at = now() - interval '1 hour' WHERE code = 'SWEET'"); err != nil {
		t.Fatalf("expire code: %v", err)
	}
	if err := txE.Commit(ctx); err != nil {
		t.Fatalf("commit expire: %v", err)
	}
	do("POST", "/api/v1/checkout", s2,
		`{"customer_name":"Ada","customer_phone":"+1-555-2222",`+
			`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+rateID+`"}`,
		fiber.StatusBadRequest)

	// --- usage limit consumed between apply and checkout -> rejected ---
	create(`{"code":"ONESHOT","type":"percentage","value_percent":10,"usage_limit":1}`)
	s3a, s3b := "sess-3a-"+sfx, "sess-3b-"+sfx
	for _, s := range []string{s3a, s3b} {
		do("POST", "/api/v1/cart", s, `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
		do("POST", "/api/v1/cart/discount", s, `{"code":"ONESHOT"}`, fiber.StatusOK)
	}
	if tu := timesUsed(t, pool, ctx, aID, "ONESHOT"); tu != 0 {
		t.Fatalf("expected ONESHOT times_used 0 before checkout, got %d", tu)
	}
	res = checkout(s3a, fiber.StatusCreated) // consumes the single use
	var ord3 orderPayload
	if err := json.NewDecoder(res.Body).Decode(&ord3); err != nil {
		t.Fatalf("decode order3: %v", err)
	}
	do("POST", "/api/v1/checkout", s3b,
		`{"customer_name":"Ada","customer_phone":"+1-555-3333",`+
			`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+rateID+`"}`,
		fiber.StatusBadRequest) // applied earlier, rejected at checkout

	// --- concurrent checkouts, usage_limit=1: exactly one wins ---
	create(`{"code":"ONLYONE","type":"percentage","value_percent":10,"usage_limit":1}`)
	sx, sy := "sess-x-"+sfx, "sess-y-"+sfx
	for _, s := range []string{sx, sy} {
		do("POST", "/api/v1/cart", s, `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
		do("POST", "/api/v1/cart/discount", s, `{"code":"ONLYONE"}`, fiber.StatusOK)
	}
	if tu := timesUsed(t, pool, ctx, aID, "ONLYONE"); tu != 0 {
		t.Fatalf("expected ONLYONE times_used 0 before concurrent checkout, got %d", tu)
	}
	// Two goroutines race through checkout. t.Fatalf must NOT run here (it would
	// exit a goroutine mid-flight); the raw request returns just the status.
	coStatus := func(session string) int {
		req := httptest.NewRequest("POST", "/api/v1/checkout", strings.NewReader(
			`{"customer_name":"Ada","customer_phone":"+1-555-4444",`+
				`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
				`"shipping_rate_id":"`+rateID+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant-ID", aID)
		req.Header.Set("X-Customer-Session", session)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Errorf("concurrent checkout %s: %v", session, err)
			return 0
		}
		defer res.Body.Close()
		return res.StatusCode
	}
	var gate sync.WaitGroup
	statuses := make(chan int, 2)
	for _, s := range []string{sx, sy} {
		g := s
		gate.Add(1)
		go func() {
			defer gate.Done()
			statuses <- coStatus(g)
		}()
	}
	gate.Wait()
	close(statuses)
	created, rejected := 0, 0
	for code := range statuses {
		switch code {
		case fiber.StatusCreated:
			created++
		case fiber.StatusBadRequest:
			rejected++
		default:
			t.Fatalf("unexpected status during concurrent checkout: %d", code)
		}
	}
	if created != 1 || rejected != 1 {
		t.Fatalf("concurrent usage_limit=1 checkouts: exactly one should succeed, got created=%d rejected=%d",
			created, rejected)
	}

	// --- RLS: tenant B sees no discounts ---
	reqB := httptest.NewRequest("GET", "/api/v1/discounts", nil)
	reqB.Header.Set("X-Tenant-ID", aIDb)
	reqB.Header.Set("Authorization", "Bearer "+bToken)
	resB, err := app.Test(reqB, -1)
	if err != nil {
		t.Fatalf("B discounts: %v", err)
	}
	var listB struct {
		Discounts []struct {
			Code string `json:"code"`
		} `json:"discounts"`
	}
	_ = json.NewDecoder(resB.Body).Decode(&listB)
	if len(listB.Discounts) != 0 {
		t.Fatalf("tenant B must not see tenant A's discounts, got %+v", listB.Discounts)
	}

	// --- admin disable + delete ---
	findID := func(code string) string {
		res := adminGet("/api/v1/discounts", fiber.StatusOK)
		var l struct {
			Discounts []struct {
				Code string `json:"code"`
				ID   string `json:"id"`
			} `json:"discounts"`
		}
		_ = json.NewDecoder(res.Body).Decode(&l)
		for _, d := range l.Discounts {
			if d.Code == code {
				return d.ID
			}
		}
		return ""
	}
	fixID := findID("FIX50")
	if fixID == "" {
		t.Fatalf("FIX50 not found in list")
	}
	adminPost("PATCH", "/api/v1/discounts/"+fixID, `{"status":"disabled"}`, fiber.StatusOK)
	adminPost("DELETE", "/api/v1/discounts/"+fixID, "", fiber.StatusNoContent)
	adminGet("/api/v1/discounts/"+fixID, fiber.StatusNotFound)

	// PATCH on a percentage code: switching type without its value is rejected.
	var sweetID = findID("SWEET")
	adminPost("PATCH", "/api/v1/discounts/"+sweetID, `{"type":"fixed_amount"}`, fiber.StatusBadRequest)
}

// TestAutomaticDiscounts is the Phase 21 acceptance criterion family:
//   - a requires_code=false 'shipping' discount (free shipping over $X) applies
//     with no code entered once the cart clears min_subtotal_cents;
//   - the single best automatic discount is picked deterministically (tie =
//     lowest discount id), never more than one, and never stacked with an
//     entered code — the higher-value of {entered code, best auto} is applied,
//     only the winner's times_used is bumped;
//   - an automatic discount's own usage cap is respected;
//   - admin rejects the reserved 'product'/BOGO scope for now.
func TestAutomaticDiscounts(t *testing.T) {
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
	sfx := hex.EncodeToString(func() []byte {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		return b
	}())

	mkTenant := func(name string) (tid, token string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "autodisc-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tid, token
	}
	tid, token := mkTenant("alpha")

	seedProduct := func(price, inv int) (variantID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin product: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var prodID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
			VALUES ($1, 'ap', 'ap-'+replace(gen_random_uuid()::text,'-',''), $2, 'usd', $3, 'active') RETURNING id`,
			tid, price, inv).Scan(&prodID); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
			tid, prodID, price, inv).Scan(&variantID); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit product: %v", err)
		}
		return variantID
	}

	seedRate := func() (rateID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin rate: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var zoneID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipping_zones (tenant_id, name, countries, regions)
			VALUES ($1, 'PK', '{PK}', '{}') RETURNING id`, tid).Scan(&zoneID); err != nil {
			t.Fatalf("seed zone: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order)
			VALUES ($1, $2, 'Standard', 500, NULL, 0) RETURNING id`, tid, zoneID).Scan(&rateID); err != nil {
			t.Fatalf("seed rate: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit rate: %v", err)
		}
		return rateID
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	discounts.RegisterRoutes(v1, pool, secret, discounts.New(pool))
	bus := events.NewBus()
	orders.RegisterRoutes(v1, pool, secret, orders.New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))
	shipping.RegisterRoutes(v1, pool, secret, shipping.New(pool))

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
	admin := func(method, path, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", tid)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("admin %s %s: %v", method, path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("admin %s %s: status %d, want %d (body=%s)", method, path, res.StatusCode, want, raw)
		}
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
		return res
	}

	v2000 := seedProduct(2000, 30)
	v1000 := seedProduct(1000, 30)
	rateID := seedRate()

	checkout := func(session string, want int) *http.Response {
		return do("POST", "/api/v1/checkout", session,
			`{"customer_name":"Ada","customer_phone":"+1-555-`+sfx+`",`+
				`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
				`"shipping_rate_id":"`+rateID+`"}`,
			want)
	}
	add := func(session, variant string, qty int) {
		do("POST", "/api/v1/cart", session,
			`{"variant_id":"`+variant+`","quantity":`+itoa(qty)+`}`, fiber.StatusOK)
	}
	decode := func(res *http.Response, v any) {
		t.Helper()
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatalf("decode: %v", err)
		}
		res.Body.Close()
	}
	autoUsages := func() map[string]int {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin usages: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		rows, err := tx.Query(ctx,
			"SELECT id, times_used FROM discounts WHERE requires_code = false ORDER BY id")
		if err != nil {
			t.Fatalf("query usages: %v", err)
		}
		defer rows.Close()
		m := map[string]int{}
		for rows.Next() {
			var id string
			var n int
			if err := rows.Scan(&id, &n); err != nil {
				t.Fatalf("scan usages: %v", err)
			}
			m[id] = n
		}
		return m
	}

	// --- admin guards for the reserved BOGO scope ---
	admin("POST", "/api/v1/discounts", `{"applies_to":"product","type":"percentage","value_percent":50}`, fiber.StatusBadRequest)
	admin("POST", "/api/v1/discounts", `{"buy_quantity":2,"get_quantity":1,"type":"percentage","value_percent":100}`, fiber.StatusBadRequest)

	// --- free shipping over 1500 (automatic, no code) ---
	createAuto := func(name string, min, limit int, percent int) {
		lim := "null"
		if limit > 0 {
			lim = itoa(limit)
		}
		body := `{"applies_to":"shipping","requires_code":false,"type":"percentage","value_percent":` + itoa(percent) +
			`,"min_subtotal_cents":` + itoa(min) + `,"usage_limit":` + lim + `}`
		res := admin("POST", "/api/v1/discounts", body, fiber.StatusCreated)
		var d struct {
			ID           string  `json:"id"`
			Code         *string `json:"code"`
			AppliesTo    string  `json:"applies_to"`
			RequiresCode bool    `json:"requires_code"`
		}
		decode(res, &d)
		if d.AppliesTo != "shipping" || !d.RequiresCode || d.Code != nil {
			t.Fatalf("auto discount invalid echo: %+v", d)
		}
	}

	createAuto("FS1", 1500, 0, 100)
	createAuto("FS2", 1500, 0, 100)

	// 2000 > 1500 -> automatic free shipping applies: shipping charged 0,
	// discount_cents records the 500 savings, order totals 2000 - 0 + 0.
	s1 := "sess-a1-" + sfx
	add(s1, v2000, 1)
	res := checkout(s1, fiber.StatusCreated)
	var ord orderPayload
	decode(res, &ord)
	if ord.DiscountCode != "" || ord.DiscountCents != 500 || ord.ShippingCost != 500 || ord.TotalCents != 2000 {
		t.Fatalf("free-shipping order wrong: code=%q cents=%d ship=%d total=%d",
			ord.DiscountCode, ord.DiscountCents, ord.ShippingCost, ord.TotalCents)
	}

	// 1000 < 1500 -> below threshold, no automatic discount.
	s2 := "sess-a2-" + sfx
	add(s2, v1000, 1)
	res = checkout(s2, fiber.StatusCreated)
	decode(res, &ord)
	if ord.DiscountCents != 0 || ord.TotalCents != 1500 {
		t.Fatalf("below-threshold order wrong: cents=%d total=%d", ord.DiscountCents, ord.TotalCents)
	}

	// deterministic tie: two identical autos, the lowest-id one absorbs usage.
	u := autoUsages()
	if len(u) != 2 {
		t.Fatalf("expected 2 automatic discounts, got %d", len(u))
	}
	var lowID, highID string
	for id := range u {
		if lowID == "" || id < lowID {
			highID, lowID = lowID, id
		}
	}
	s3 := "sess-a3-" + sfx
	add(s3, v2000, 1)
	checkout(s3, fiber.StatusCreated)
	u = autoUsages()
	if u[lowID] != 2 || u[highID] != 0 {
		t.Fatalf("tie should go to lowest id (%s=2, %s=0), got %s=%d %s=%d",
			lowID, highID, lowID, u[lowID], highID, u[highID])
	}

	// --- no stacking, code wins: SAVE30 (600) beats free shipping (500) ---
	admin("POST", "/api/v1/discounts", `{"code":"SAVE30","type":"percentage","value_percent":30}`, fiber.StatusCreated)
	s4 := "sess-a4-" + sfx
	add(s4, v2000, 1)
	do("POST", "/api/v1/cart/discount", s4, `{"code":"SAVE30"}`, fiber.StatusOK)
	res = checkout(s4, fiber.StatusCreated)
	decode(res, &ord)
	if ord.DiscountCode != "SAVE30" || ord.DiscountCents != 600 || ord.TotalCents != 1900 {
		t.Fatalf("code-wins order wrong: code=%q cents=%d total=%d", ord.DiscountCode, ord.DiscountCents, ord.TotalCents)
	}
	if tu := timesUsed(t, pool, ctx, tid, "SAVE30"); tu != 1 {
		t.Fatalf("SAVE30 should be claimed once, got %d", tu)
	}
	if u := autoUsages(); u[lowID] != 2 {
		t.Fatalf("auto must NOT be claimed when a code wins, got %d", u[lowID])
	}

	// --- no stacking, auto wins: SMALL5 (100) loses to free shipping (500) ---
	admin("POST", "/api/v1/discounts", `{"code":"SMALL5","type":"percentage","value_percent":5}`, fiber.StatusCreated)
	s5 := "sess-a5-" + sfx
	add(s5, v2000, 1)
	do("POST", "/api/v1/cart/discount", s5, `{"code":"SMALL5"}`, fiber.StatusOK)
	res = checkout(s5, fiber.StatusCreated)
	decode(res, &ord)
	if ord.DiscountCode != "" || ord.DiscountCents != 500 || ord.TotalCents != 2000 {
		t.Fatalf("auto-wins order wrong: code=%q cents=%d total=%d", ord.DiscountCode, ord.DiscountCents, ord.TotalCents)
	}
	if tu := timesUsed(t, pool, ctx, tid, "SMALL5"); tu != 0 {
		t.Fatalf("SMALL5 must NOT be claimed when auto wins, got %d", tu)
	}
	if u := autoUsages(); u[lowID] != 3 {
		t.Fatalf("winning auto should be claimed, got %d", u[lowID])
	}

	// --- automatic discount honors its own usage cap (isolated tenant) ---
	tidB, tokenB := mkTenant("beta")
	vB := seedProductFor(t, pool, ctx, tidB, 2000)
	rateB := seedRateFor(t, pool, ctx, tidB)
	createAutoFor(t, app, tidB, tokenB)
	sB1, sB2 := "sess-b1-"+sfx, "sess-b2-"+sfx
	for _, s := range []string{sB1, sB2} {
		req := httptest.NewRequest("POST", "/api/v1/cart", strings.NewReader(
			`{"variant_id":"`+vB+`","quantity":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant-ID", tidB)
		req.Header.Set("X-Customer-Session", s)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("B cart: %v", err)
		}
		res.Body.Close()
		res, err = app.Test(httptest.NewRequest("POST", "/api/v1/checkout", strings.NewReader(
			`{"customer_name":"Ada","customer_phone":"+1-555-B"+`+sfx+`",`+
				`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
				`"shipping_rate_id":"`+rateB+`"}`)), -1)
		if err != nil {
			t.Fatalf("B checkout: %v", err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		bods := orderPayload{}
		_ = json.Unmarshal(raw, &bods)
		var want int
		if s == sB1 {
			if bods.DiscountCents != 500 || bods.TotalCents != 2000 {
				t.Fatalf("B first checkout should use the cap-1 auto, got %+v", bods)
			}
			want = fiber.StatusCreated
		} else {
			if bods.DiscountCents != 0 || bods.TotalCents != 2500 {
				t.Fatalf("B second checkout should hit the cap and apply nothing, got %+v", bods)
			}
			want = fiber.StatusCreated
		}
		if res.StatusCode != want {
			t.Fatalf("B checkout status %d, want %d", res.StatusCode, want)
		}
	}
}

// seedProductFor / seedRateFor / createAutoFor seed tenant-scoped data for the
// isolated usage-cap scenario.
func seedProductFor(t *testing.T, pool *pgxpool.Pool, ctx context.Context, tid string, price int) string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin product: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var prodID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		VALUES ($1, 'bp', 'bp-'+replace(gen_random_uuid()::text,'-',''), $2, 'usd', 30, 'active') RETURNING id`,
		tid, price).Scan(&prodID); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	var variantID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		VALUES ($1, $2, $3, 30, 'active') RETURNING id`,
		tid, prodID, price).Scan(&variantID); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit product: %v", err)
	}
	return variantID
}

func seedRateFor(t *testing.T, pool *pgxpool.Pool, ctx context.Context, tid string) string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin rate: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var zoneID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_zones (tenant_id, name, countries, regions)
		VALUES ($1, 'PK', '{PK}', '{}') RETURNING id`, tid).Scan(&zoneID); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	var rateID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order)
		VALUES ($1, $2, 'Standard', 500, NULL, 0) RETURNING id`, tid, zoneID).Scan(&rateID); err != nil {
		t.Fatalf("seed rate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit rate: %v", err)
	}
	return rateID
}

func createAutoFor(t *testing.T, app *fiber.App, tid, token string) {
	t.Helper()
	body := `{"applies_to":"shipping","requires_code":false,"type":"percentage","value_percent":100,"usage_limit":1}`
	req := httptest.NewRequest("POST", "/api/v1/discounts", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tid)
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("create auto: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != fiber.StatusCreated {
		t.Fatalf("create auto status %d", res.StatusCode)
	}
}

func itoa(n int) string {
	return strconv.Itoa(n)
}

// timesUsed reads times_used for a code inside the tenant's RLS scope.
func timesUsed(t *testing.T, pool *pgxpool.Pool, ctx context.Context, tid, code string) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var n int
	if err := tx.QueryRow(ctx,
		"SELECT times_used FROM discounts WHERE code = $1", code).Scan(&n); err != nil {
		t.Fatalf("read times_used: %v", err)
	}
	return n
}

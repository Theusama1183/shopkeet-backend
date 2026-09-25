package orders

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
)

func randSuffix5() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type orderPayload struct {
	ID                string `json:"id"`
	Status            string `json:"status"`
	PaymentStatus     string `json:"payment_status"`
	PaymentMethod     string `json:"payment_method"`
	TotalCents        int    `json:"total_cents"`
	Currency          string `json:"currency"`
	ShippingMethod    string `json:"shipping_method"`
	ShippingCostCents int    `json:"shipping_cost_cents"`
	Items             []struct {
		ProductID      string `json:"product_id"`
		VariantID      string `json:"variant_id"`
		Quantity       int    `json:"quantity"`
		UnitPriceCents int    `json:"unit_price_cents"`
	} `json:"items"`
}

type tenantO struct {
	id    string
	token string
}

// TestOrdersRLSIsolation is the Phase 5 acceptance criterion (extended by
// Phase 8 for variants and Phase 9 for shipping): a guest in merchant A's store
// completes COD checkout, choosing a shipping rate that is resolved against the
// destination (cost + rate name snapshotted; the free-over threshold and a
// mismatched destination are covered in the shipping package tests); inventory
// decrements exactly once per variant; a concurrent-style second cart for the
// last unit of one variant is refused at checkout (not at add-to-cart time)
// while the product's other variant stays independently purchasable; the
// merchant lists orders and walks one to delivered+paid (emitting order.created
// / order.paid); tenant B sees nothing.
func TestOrdersRLSIsolation(t *testing.T) {
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

	mkTenant := func(name string) tenantO {
		var tid string
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "ord-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tenantO{id: tid, token: token}
	}
	a := mkTenant("alpha")
	b := mkTenant("beta")

	seedProduct := func(tid, slug string, price, inv int, status string) (prodID, variantID string) {
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
			VALUES ($1, $2, $3, $4, 'usd', $5, $6) RETURNING id`,
			tid, slug, "ord-"+slug+"-"+sfx, price, inv, status).Scan(&prodID); err != nil {
			t.Fatalf("seed product %s: %v", slug, err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
			tid, prodID, price, inv).Scan(&variantID); err != nil {
			t.Fatalf("seed variant %s: %v", slug, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return prodID, variantID
	}
	// p1 has TWO independently-priced variants (Phase 8): v1a (3 units) and
	// v1b (2 units). Exhausting v1a must not take v1b with it.
	p1, v1a := seedProduct(a.id, "p1", 1000, 3, "active")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin v1b: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", a.id); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var v1b string
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
		a.id, p1, 1200, 2).Scan(&v1b); err != nil {
		t.Fatalf("seed v1b: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit v1b: %v", err)
	}
	p2, v2 := seedProduct(a.id, "p2", 250, 1, "active")

	// Phase 9: one zone covering country PK with two rates — Standard 500
	// (free over 5000) and Express 1200, no threshold.
	seedZone := func(tid, name string, countries, regions []string) (zoneID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin zone: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipping_zones (tenant_id, name, countries, regions)
			VALUES ($1, $2, $3, $4) RETURNING id`,
			tid, name, countries, regions).Scan(&zoneID); err != nil {
			t.Fatalf("seed zone: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit zone: %v", err)
		}
		return zoneID
	}
	seedRate := func(tid, zoneID, name string, rateCents int, freeOver *int) (rateID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin rate: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order)
			VALUES ($1, $2, $3, $4, $5, 0) RETURNING id`,
			tid, zoneID, name, rateCents, freeOver).Scan(&rateID); err != nil {
			t.Fatalf("seed rate: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit rate: %v", err)
		}
		return rateID
	}
	zonePK := seedZone(a.id, "Pakistan", []string{"PK", "IN"}, []string{})
	freeOver := 5000
	stdRate := seedRate(a.id, zonePK, "Standard", 500, &freeOver)
	seedRate(a.id, zonePK, "Express", 1200, nil)
	zoneUS := seedZone(a.id, "USA", []string{"US"}, []string{})
	usRate := seedRate(a.id, zoneUS, "US Standard", 900, nil)

	// Event bus records order.created / order.paid payloads.
	bus := events.NewBus()
	var mu sync.Mutex
	created, paid := []string{}, []string{}
	bus.Subscribe("order.created", func(_ context.Context, e events.Event) error {
		mu.Lock()
		created = append(created, e.Data.(fiber.Map)["order_id"].(string))
		mu.Unlock()
		return nil
	})
	bus.Subscribe("order.paid", func(_ context.Context, e events.Event) error {
		mu.Lock()
		paid = append(paid, e.Data.(fiber.Map)["order_id"].(string))
		mu.Unlock()
		return nil
	})

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, cart.New(pool, cart.NoopReserver{}))
	RegisterRoutes(v1, pool, secret, New(pool, bus, payments.NewRegistry()))

	do := func(method, path, session, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", a.id)
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
	adminToken := a.token

	// Two guests race for the same 3 units of variant v1a (2+2) plus p2/v2
	// (1 of 1); a third guest shops the independent variant v1b.
	s1 := "sess-one-" + sfx
	s2 := "sess-two-" + sfx
	s3 := "sess-three-" + sfx
	do("POST", "/api/v1/cart", s1, `{"variant_id":"`+v1a+`","quantity":2}`, fiber.StatusOK)
	do("POST", "/api/v1/cart", s1, `{"variant_id":"`+v2+`","quantity":1}`, fiber.StatusOK)
	do("POST", "/api/v1/cart", s2, `{"variant_id":"`+v1a+`","quantity":2}`, fiber.StatusOK)

	phone := "+1-555-" + sfx
	// Guest 1 completes COD checkout with a chosen shipping rate (Standard 500,
	// subtotal 2250 < free-over 5000 → paid).
	co := do("POST", "/api/v1/checkout", s1,
		`{"customer_name":"Ada","customer_phone":"`+phone+`","customer_email":"ada@example.com",`+
			`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+stdRate+`"}`,
		fiber.StatusCreated)
	var ord orderPayload
	if err := json.NewDecoder(co.Body).Decode(&ord); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if ord.Status != "pending" || ord.PaymentStatus != "pending" || ord.PaymentMethod != "cod" {
		t.Fatalf("new order should be pending/pending/cod, got %+v", ord)
	}
	if ord.TotalCents != 2750 || len(ord.Items) != 2 || ord.Currency != "usd" {
		t.Fatalf("order should total 2750 (2250 items + 500 shipping) over 2 items, got %+v", ord)
	}
	if ord.ShippingMethod != "Standard" || ord.ShippingCostCents != 500 {
		t.Fatalf("order should snapshot shipping_method=Standard cost=500, got method=%q cost=%d",
			ord.ShippingMethod, ord.ShippingCostCents)
	}

	// Inventory decremented exactly once, per variant; the cached products
	// aggregate follows (Phase 8).
	variantInv := func(id string) int {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", a.id)
		var n int
		if err := tx.QueryRow(ctx,
			"SELECT inventory_count FROM product_variants WHERE id = $1", id).Scan(&n); err != nil {
			t.Fatalf("read variant inventory: %v", err)
		}
		return n
	}
	productInv := func(id string) int {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", a.id)
		var n int
		if err := tx.QueryRow(ctx,
			"SELECT inventory_count FROM products WHERE id = $1", id).Scan(&n); err != nil {
			t.Fatalf("read product inventory: %v", err)
		}
		return n
	}
	if v := variantInv(v1a); v != 1 {
		t.Fatalf("v1a inventory should be 1 after checkout, got %d", v)
	}
	if v := variantInv(v1b); v != 2 {
		t.Fatalf("v1b inventory should be untouched (2), got %d", v)
	}
	if v := variantInv(v2); v != 0 {
		t.Fatalf("v2 inventory should be 0 after checkout, got %d", v)
	}
	// products cache = SUM of its active variants.
	if v := productInv(p1); v != variantInv(v1a)+variantInv(v1b) {
		t.Fatalf("p1 cached inventory should equal sum of variants, got %d", v)
	}
	if v := productInv(p2); v != 0 {
		t.Fatalf("p2 cached inventory should be 0, got %d", v)
	}

	// Guest 1's cart is cleared.
	var emptyCart struct {
		Cart *json.RawMessage `json:"cart"`
	}
	res := do("GET", "/api/v1/cart", s1, "", fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&emptyCart); err != nil {
		t.Fatalf("decode cleared cart: %v", err)
	}
	if emptyCart.Cart != nil {
		t.Fatalf("cart should be cleared after checkout, got %+v (== %s)", emptyCart.Cart, string(*emptyCart.Cart))
	}

	// The second guest can no longer get v1a at checkout: 409 at checkout, not
	// at add-to-cart time. (Valid address + rate so the failure is stock, not
	// shipping validation.)
	do("POST", "/api/v1/checkout", s2,
		`{"customer_name":"Grace","customer_phone":"+1-555-0000",`+
			`"shipping_address_line1":"2 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+stdRate+`"}`,
		fiber.StatusConflict)

	// order.created fired once for this order.
	mu.Lock()
	if len(created) != 1 || created[0] != ord.ID {
		t.Fatalf("expected one order.created for %s, got %v", ord.ID, created)
	}
	mu.Unlock()

	// Guest 3 buys the OTHER variant of the same product: with v1a exhausted,
	// v1b stays independently purchasable (Phase 8 acceptance — variants have
	// their own price and inventory).
	do("POST", "/api/v1/cart", s3, `{"variant_id":"`+v1b+`","quantity":1}`, fiber.StatusOK)
	co3 := do("POST", "/api/v1/checkout", s3,
		`{"customer_name":"Lin","customer_phone":"+1-555-3333",`+
			`"shipping_address_line1":"3 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+stdRate+`"}`,
		fiber.StatusCreated)
	var ord2 orderPayload
	if err := json.NewDecoder(co3.Body).Decode(&ord2); err != nil {
		t.Fatalf("decode order2: %v", err)
	}
	if ord2.TotalCents != 1700 || len(ord2.Items) != 1 {
		t.Fatalf("v1b order should total 1700 (1200 item + 500 shipping) over 1 item, got %+v", ord2)
	}
	if ord2.ShippingMethod != "Standard" || ord2.ShippingCostCents != 500 {
		t.Fatalf("order2 should snapshot Standard/500, got method=%q cost=%d",
			ord2.ShippingMethod, ord2.ShippingCostCents)
	}
	if ord2.Items[0].VariantID != v1b {
		t.Fatalf("v1b order should reference variant v1b, got %+v", ord2.Items[0])
	}
	if v := variantInv(v1b); v != 1 {
		t.Fatalf("v1b inventory should be 1 after guest 3, got %d", v)
	}
	mu.Lock()
	if len(created) != 2 || created[1] != ord2.ID {
		t.Fatalf("expected two order.created events, got %v", created)
	}
	mu.Unlock()

	// Phase 9 free-over waive: a subtotal at/above the Standard threshold pays 0.
	_, v3 := seedProduct(a.id, "p3", 6000, 2, "active")
	s4, s5 := "sess-four-"+sfx, "sess-five-"+sfx
	do("POST", "/api/v1/cart", s4, `{"variant_id":"`+v3+`","quantity":1}`, fiber.StatusOK)
	co4 := do("POST", "/api/v1/checkout", s4,
		`{"customer_name":"Nina","customer_phone":"+1-555-4444",`+
			`"shipping_address_line1":"4 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+stdRate+`"}`,
		fiber.StatusCreated)
	var ord4 orderPayload
	if err := json.NewDecoder(co4.Body).Decode(&ord4); err != nil {
		t.Fatalf("decode order4: %v", err)
	}
	if ord4.TotalCents != 6000 || ord4.ShippingCostCents != 0 || ord4.ShippingMethod != "Standard" {
		t.Fatalf("free-over order should total 6000 with 0 shipping, got %+v", ord4)
	}

	// Phase 9 destination mismatch: a rate from a US-only zone is rejected for a
	// PK destination (and the cart survives to pay later).
	do("POST", "/api/v1/cart", s5, `{"variant_id":"`+v3+`","quantity":1}`, fiber.StatusOK)
	do("POST", "/api/v1/checkout", s5,
		`{"customer_name":"Omar","customer_phone":"+1-555-5555",`+
			`"shipping_address_line1":"5 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+usRate+`"}`,
		fiber.StatusBadRequest)
	// v3 still has the second unit — the failed checkout consumed nothing.
	if v := variantInv(v3); v != 1 {
		t.Fatalf("v3 inventory should stay 1 after rejected checkout, got %d", v)
	}

	// Guest lookup by id + phone (+ email). url.QueryEscape keeps the "+" from a
	// phone number from being decoded as a space in the query string.
	encPhone := url.QueryEscape(phone)
	do("GET", "/api/v1/orders/"+ord.ID+"?phone="+encPhone, s1, "", fiber.StatusOK)
	do("GET", "/api/v1/orders/"+ord.ID+"?phone="+encPhone+"&email=ada@example.com", s1, "", fiber.StatusOK)
	do("GET", "/api/v1/orders/"+ord.ID+"?phone="+url.QueryEscape("+1-000-0000000"), s1, "", fiber.StatusNotFound)
	do("GET", "/api/v1/orders/"+ord.ID+"?phone="+encPhone+"&email=wrong@example.com", s1, "", fiber.StatusNotFound)

	// Admin list + status filter.
	adminGet := func(path string, want int) *http.Response {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Tenant-ID", a.id)
		req.Header.Set("Authorization", "Bearer "+adminToken)
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
	var list struct {
		Orders []orderPayload `json:"orders"`
	}
	res = adminGet("/api/v1/orders", fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode admin orders: %v", err)
	}
	if len(list.Orders) != 3 {
		t.Fatalf("admin sees exactly 3 orders (ours), got %+v", list.Orders)
	}
	var ord1Founded *orderPayload
	for i := range list.Orders {
		if list.Orders[i].ID == ord.ID {
			ord1Founded = &list.Orders[i]
		}
	}
	if ord1Founded == nil {
		t.Fatalf("admin list missing ord1 %s, got %+v", ord.ID, list.Orders)
	}
	if len(ord1Founded.Items) != 2 {
		t.Fatalf("admin order list missing items, got %+v", ord1Founded)
	}
	res = adminGet("/api/v1/orders?status=pending", fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&list)
	if len(list.Orders) != 3 {
		t.Fatalf("filter status=pending should match 3, got %d", len(list.Orders))
	}
	res = adminGet("/api/v1/orders?status=shipped", fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&list)
	if len(list.Orders) != 0 {
		t.Fatalf("filter status=shipped should match 0, got %d", len(list.Orders))
	}

	adminPatch := func(status string, want int) orderPayload {
		t.Helper()
		req := httptest.NewRequest("PATCH", "/api/v1/orders/"+ord.ID+"/status",
			strings.NewReader(`{"status":"`+status+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant-ID", a.id)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("patch status %s: %v", status, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("patch status %s: status %d, want %d (body=%s)", status, res.StatusCode, want, raw)
		}
		res.Body = io.NopCloser(strings.NewReader(string(raw)))
		var o orderPayload
		if want == fiber.StatusOK {
			if err := json.NewDecoder(res.Body).Decode(&o); err != nil {
				t.Fatalf("decode patched order: %v", err)
			}
		}
		return o
	}

	// Skip-a-step is rejected; forward chain then walks to delivered → paid.
	adminPatch("delivered", fiber.StatusBadRequest)
	adminPatch("confirmed", fiber.StatusOK)
	adminPatch("confirmed", fiber.StatusBadRequest) // can't stay
	od := adminPatch("shipped", fiber.StatusOK)
	if od.PaymentStatus != "pending" {
		t.Fatalf("shipped should still be pending payment, got %s", od.PaymentStatus)
	}
	od = adminPatch("delivered", fiber.StatusOK)
	if od.Status != "delivered" || od.PaymentStatus != "paid" {
		t.Fatalf("delivered should set payment_status=paid, got %+v", od)
	}
	mu.Lock()
	if len(paid) != 1 || paid[0] != ord.ID {
		t.Fatalf("expected one order.paid for %s, got %v", ord.ID, paid)
	}
	mu.Unlock()

	// --- Isolation -----------------------------------------------------------
	// Tenant B's admin sees zero orders.
	reqB := httptest.NewRequest("GET", "/api/v1/orders", nil)
	reqB.Header.Set("X-Tenant-ID", b.id)
	reqB.Header.Set("Authorization", "Bearer "+b.token)
	resB, err := app.Test(reqB, -1)
	if err != nil {
		t.Fatalf("B admin orders: %v", err)
	}
	rawB, _ := io.ReadAll(resB.Body)
	resB.Body.Close()
	var listB struct {
		Orders []orderPayload `json:"orders"`
	}
	if err := json.Unmarshal(rawB, &listB); err != nil {
		t.Fatalf("decode B orders: %v", err)
	}
	if len(listB.Orders) != 0 {
		t.Fatalf("tenant B admin must not see tenant A's orders, got %+v", listB.Orders)
	}

	// The same customer session+phone in tenant B cannot look up A's order.
	reqB2 := httptest.NewRequest("GET", "/api/v1/orders/"+ord.ID+"?phone="+encPhone, nil)
	reqB2.Header.Set("X-Tenant-ID", b.id)
	reqB2.Header.Set("X-Customer-Session", s1)
	resB2, err := app.Test(reqB2, -1)
	if err != nil {
		t.Fatalf("B customer order lookup: %v", err)
	}
	rawB2, _ := io.ReadAll(resB2.Body)
	resB2.Body.Close()
	if resB2.StatusCode != fiber.StatusNotFound {
		t.Fatalf("B customer fetching A's order should 404, got %d (%s)", resB2.StatusCode, rawB2)
	}
}

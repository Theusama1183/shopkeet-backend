package orders

import (
	"context"
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
	"github.com/shopkeet/api/internal/platform/cache"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// invalidationRecorder is a cache.Cache that records every Del key so tests
// assert the storefront product cache is dropped after draft/restock writes.
type invalidationRecorder struct {
	mu  sync.Mutex
	del map[string]int
}

func (r *invalidationRecorder) Del(_ context.Context, key string) {
	r.mu.Lock()
	if r.del == nil {
		r.del = map[string]int{}
	}
	r.del[key]++
	r.mu.Unlock()
}
func (r *invalidationRecorder) DelPrefix(context.Context, string) {}
func (r *invalidationRecorder) Get(context.Context, string) ([]byte, bool) {
	return nil, false
}
func (r *invalidationRecorder) Set(context.Context, string, []byte, time.Duration) {}
func (r *invalidationRecorder) Close() error                                       { return nil }
func (r *invalidationRecorder) count(key string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.del[key]
}

var _ cache.Cache = (*invalidationRecorder)(nil)

type draftPayload struct {
	ID            string `json:"id"`
	Source        string `json:"source"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
	TotalCents    int    `json:"total_cents"`
	Items         []struct {
		VariantID      string `json:"variant_id"`
		UnitPriceCents int    `json:"unit_price_cents"`
	} `json:"items"`
}

type orderItemPayload struct {
	ID             string `json:"id"`
	ProductID      string `json:"product_id"`
	VariantID      string `json:"variant_id"`
	Quantity       int    `json:"quantity"`
	UnitPriceCents int    `json:"unit_price_cents"`
}

type richOrderPayload struct {
	ID     string             `json:"id"`
	Status string             `json:"status"`
	Items  []orderItemPayload `json:"items"`
}

type returnPayload struct {
	ID      string `json:"id"`
	OrderID string `json:"order_id"`
	Status  string `json:"status"`
	Restock bool   `json:"restock"`
	Items   []struct {
		ID          string `json:"id"`
		OrderItemID string `json:"order_item_id"`
		VariantID   string `json:"variant_id"`
		ProductID   string `json:"product_id"`
		Quantity    int    `json:"quantity"`
	} `json:"items"`
}

// seedReturnProduct creates one product with a single 1000-price variant
// (inventory 8) plus an independent second variant (500, inventory 5).
func seedReturnProduct(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tid, slug, sfx string) (p1, v1a, v1b string) {
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
	if err := tx.QueryRow(ctx, `
		INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'usd', $5, 'active') RETURNING id`,
		tid, slug, "ret-"+slug+"-"+sfx, 1000, 13).Scan(&p1); err != nil {
		t.Fatalf("seed product %s: %v", slug, err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
		tid, p1, 1000, 8).Scan(&v1a); err != nil {
		t.Fatalf("seed v1a: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
		tid, p1, 500, 5).Scan(&v1b); err != nil {
		t.Fatalf("seed v1b: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return p1, v1a, v1b
}

func seedReturnRate(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tid string) string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin zone: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var zoneID, rateID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_zones (tenant_id, name, countries, regions) VALUES ($1, $2, $3, $4)
		RETURNING id`, tid, "Pakistan", []string{"PK"}, []string{}).Scan(&zoneID); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order)
		VALUES ($1, $2, $3, $4, $5, 0) RETURNING id`,
		tid, zoneID, "Standard", 500, 5000).Scan(&rateID); err != nil {
		t.Fatalf("seed rate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit zone: %v", err)
	}
	return rateID
}

func variantInv(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tid, id string) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid)
	var n int
	if err := tx.QueryRow(ctx,
		"SELECT inventory_count FROM product_variants WHERE id = $1", id).Scan(&n); err != nil {
		t.Fatalf("read variant inventory: %v", err)
	}
	return n
}

// TestDraftOrderDecrementsStock is the Phase 15 draft acceptance: a merchant
// POST /orders/draft creates a real order (source='draft') that snapshots
// per-line prices (override wins over the live price), applies shipping/tax
// like checkout, decrements inventory exactly once per variant, refreshes the
// products aggregates, drops the cached product detail, and refuses oversell /
// cross-tenant lines.
func TestDraftOrderDecrementsStock(t *testing.T) {
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
			name, "mo-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		if _, err := pool.Exec(ctx, "UPDATE tenants SET tax_rate_percent = 20 WHERE id = $1", tid); err != nil {
			t.Fatalf("set tax %s: %v", name, err)
		}
		return tenantO{id: tid, token: token}
	}
	a := mkTenant("alpha")
	b := mkTenant("beta")

	p1, v1a, v1b := seedReturnProduct(t, ctx, pool, a.id, "p1", sfx)
	_, _, vB := seedReturnProduct(t, ctx, pool, b.id, "pB", sfx)
	stdRate := seedReturnRate(t, ctx, pool, a.id)

	// An existing customer to attach (Phase 11 table; RLS-scoped insert).
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin customer: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", a.id); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var customerID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO customers (tenant_id, email, phone) VALUES ($1, $2, $3) RETURNING id`,
		a.id, "draft-buyer-"+sfx+"@example.com", "+1-draft-"+sfx).Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit customer: %v", err)
	}

	bus := events.NewBus()
	var mu sync.Mutex
	created := []fiber.Map{}
	bus.Subscribe("order.created", func(_ context.Context, e events.Event) error {
		mu.Lock()
		created = append(created, e.Data.(fiber.Map))
		mu.Unlock()
		return nil
	})
	rec := &invalidationRecorder{}
	svc := New(pool, bus, payments.NewRegistry())
	svc.SetCache(rec)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, secret, svc, ratelimit.New(nil))

	do := func(method, path, bearer, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", a.id)
		req.Header.Set("Authorization", "Bearer "+bearer)
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
	phone := "+92-3xx-" + sfx

	res := do("POST", "/api/v1/orders/draft", a.token, `{
		"customer_id":"`+customerID+`",
		"customer_name":"Ada","customer_phone":"`+phone+`","customer_email":"ada@example.com",
		"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",
		"shipping_rate_id":"`+stdRate+`",
		"lines":[
			{"variant_id":"`+v1a+`","quantity":2,"unit_price_cents":900},
			{"variant_id":"`+v1b+`","quantity":1}
		]}`, fiber.StatusCreated)
	var draft draftPayload
	if err := json.NewDecoder(res.Body).Decode(&draft); err != nil {
		t.Fatalf("decode draft: %v", err)
	}
	if draft.Source != "draft" || draft.Status != "pending" || draft.PaymentStatus != "pending" {
		t.Fatalf("draft should be pending/pending/draft, got %+v", draft)
	}
	if draft.TotalCents != 3260 { // 2×900 + 1×500 = 2300, +20% tax = 460, +500 shipping
		t.Fatalf("draft total should be 3260, got %d", draft.TotalCents)
	}
	if len(draft.Items) != 2 {
		t.Fatalf("draft should have 2 items, got %d", len(draft.Items))
	}
	// Override wins for v1a; bare v1b falls back to live 500.
	for _, it := range draft.Items {
		switch it.VariantID {
		case v1a:
			if it.UnitPriceCents != 900 {
				t.Fatalf("v1a should snapshot the override 900, got %d", it.UnitPriceCents)
			}
		case v1b:
			if it.UnitPriceCents != 500 {
				t.Fatalf("v1b should snapshot the live price 500, got %d", it.UnitPriceCents)
			}
		}
	}

	if v := variantInv(t, ctx, pool, a.id, v1a); v != 6 {
		t.Fatalf("v1a should be 6 after the draft, got %d", v)
	}
	if v := variantInv(t, ctx, pool, a.id, v1b); v != 4 {
		t.Fatalf("v1b should be 4 after the draft, got %d", v)
	}
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", a.id)
	var productInv int
	if err := tx.QueryRow(ctx,
		"SELECT inventory_count FROM products WHERE id = $1", p1).Scan(&productInv); err != nil {
		t.Fatalf("read product inventory: %v", err)
	}
	tx.Rollback(ctx)
	if productInv != 10 {
		t.Fatalf("p1 aggregate should be refreshed to 10, got %d", productInv)
	}
	publicKey := "shopkeet:cache:product:" + a.id + "/" + p1 + "/public"
	// Both draft lines touch p1, so the cache is invalidated per line (checkout
	// behaves identically).
	if c := rec.count(publicKey); c != 2 {
		t.Fatalf("draft should invalidate the cached product once per line (2), got %d", c)
	}

	mu.Lock()
	evtCount := len(created)
	evtSource := ""
	if evtCount > 0 {
		if s, ok := created[evtCount-1]["source"].(string); ok {
			evtSource = s
		}
	}
	mu.Unlock()
	if evtCount != 1 || evtSource != "draft" {
		t.Fatalf("expected one order.created(draft), got %d source=%q", evtCount, evtSource)
	}

	// Oversell is refused at creation — same FOR UPDATE guard as checkout.
	do("POST", "/api/v1/orders/draft", a.token, `{
		"customer_name":"Ada","customer_phone":"`+phone+`",
		"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",
		"shipping_rate_id":"`+stdRate+`",
		"lines":[{"variant_id":"`+v1a+`","quantity":100}]}`,
		fiber.StatusConflict)
	// Duplicate variant lines are rejected.
	do("POST", "/api/v1/orders/draft", a.token, `{
		"customer_name":"Ada","customer_phone":"`+phone+`",
		"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",
		"shipping_rate_id":"`+stdRate+`",
		"lines":[{"variant_id":"`+v1a+`","quantity":1},{"variant_id":"`+v1a+`","quantity":1}]}`,
		fiber.StatusBadRequest)
	// A tenant-B variant is invisible inside tenant A's RLS scope → 404.
	do("POST", "/api/v1/orders/draft", a.token, `{
		"customer_name":"Ada","customer_phone":"`+phone+`",
		"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",
		"shipping_rate_id":"`+stdRate+`",
		"lines":[{"variant_id":"`+vB+`","quantity":1}]}`,
		fiber.StatusNotFound)
	// Missing/garbage auth is refused by TenantMW.
	do("POST", "/api/v1/orders/draft", "not-a-token", `{
		"customer_name":"Ada","customer_phone":"`+phone+`",
		"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",
		"shipping_rate_id":"`+stdRate+`",
		"lines":[{"variant_id":"`+v1a+`","quantity":1}]}`,
		fiber.StatusUnauthorized)

	// The admin order list surfaces the draft with its source.
	res = do("GET", "/api/v1/orders", a.token, "", fiber.StatusOK)
	var list struct {
		Orders []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"orders"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode order list: %v", err)
	}
	foundDraft := false
	for _, o := range list.Orders {
		if o.ID == draft.ID {
			foundDraft = true
			if o.Source != "draft" {
				t.Fatalf("order %s should surface source=draft, got %q", o.ID, o.Source)
			}
		}
	}
	if !foundDraft {
		t.Fatalf("admin order list should contain the draft %s", draft.ID)
	}
}

// TestReturnRestocksCorrectVariant is the Phase 15 returns acceptance: a
// customer files a return on a COD order (verified by phone[/email], not a
// session); the merchant walks it requested → approved → received, and marking
// it received restocks the returned variant only — other variants and other
// tenants are untouched. Rejected returns never restock; received can advance
// to refunded without a second restock.
func TestReturnRestocksCorrectVariant(t *testing.T) {
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
			name, "ret-"+name+"-"+sfx).Scan(&tid)
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

	p1, v1a, v1b := seedReturnProduct(t, ctx, pool, a.id, "p1", sfx)
	stdRate := seedReturnRate(t, ctx, pool, a.id)

	bus := events.NewBus()
	var mu sync.Mutex
	createdReturns := []string{}
	restocked := []string{}
	bus.Subscribe("return.created", func(_ context.Context, e events.Event) error {
		mu.Lock()
		createdReturns = append(createdReturns, e.Data.(fiber.Map)["return_id"].(string))
		mu.Unlock()
		return nil
	})
	bus.Subscribe("return.restocked", func(_ context.Context, e events.Event) error {
		mu.Lock()
		restocked = append(restocked, e.Data.(fiber.Map)["return_id"].(string))
		mu.Unlock()
		return nil
	})
	rec := &invalidationRecorder{}
	svc := New(pool, bus, payments.NewRegistry())
	svc.SetCache(rec)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	RegisterRoutes(v1, pool, secret, svc, ratelimit.New(nil))

	do := func(method, path, tenantID, session, bearer, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", tenantID)
		if session != "" {
			req.Header.Set("X-Customer-Session", session)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
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

	phone := "+92-31x-" + sfx
	qPhone := url.QueryEscape(phone)
	s := "sess-return-" + sfx
	do("POST", "/api/v1/cart", a.id, s, "", `{"variant_id":"`+v1a+`","quantity":2}`, fiber.StatusOK)
	do("POST", "/api/v1/cart", a.id, s, "", `{"variant_id":"`+v1b+`","quantity":1}`, fiber.StatusOK)
	res := do("POST", "/api/v1/checkout", a.id, s, "", `{
		"customer_name":"Ada","customer_phone":"`+phone+`","customer_email":"ada@example.com",
		"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",
		"shipping_rate_id":"`+stdRate+`"}`, fiber.StatusCreated)
	var ord richOrderPayload
	if err := json.NewDecoder(res.Body).Decode(&ord); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if ord.Status != "pending" {
		t.Fatalf("order should be pending, got %q", ord.Status)
	}
	var itA, itB orderItemPayload
	for _, it := range ord.Items {
		switch it.VariantID {
		case v1a:
			itA = it
		case v1b:
			itB = it
		}
	}
	if itA.ID == "" || itA.Quantity != 2 || itB.ID == "" || itB.Quantity != 1 {
		t.Fatalf("checkout should carry v1a×2 + v1b×1, got %+v", ord.Items)
	}

	// Wrong phone on the same order id sees nothing (404).
	do("POST", "/api/v1/orders/"+ord.ID+"/returns?phone=999", a.id, "", "", `{
		"items":[{"order_item_id":"`+itA.ID+`","quantity":1}]}`,
		fiber.StatusNotFound)
	// No phone at all → 400 (the customer lookup requires it).
	do("POST", "/api/v1/orders/"+ord.ID+"/returns", a.id, "", "", `{
		"items":[{"order_item_id":"`+itA.ID+`","quantity":1}]}`,
		fiber.StatusBadRequest)

	// Correct phone + matching email → the return is born requested.
	res = do("POST", "/api/v1/orders/"+ord.ID+"/returns?phone="+qPhone+"&email=ada@example.com", a.id, "", "", `{
		"reason":"damaged","items":[{"order_item_id":"`+itA.ID+`","quantity":2}]}`,
		fiber.StatusCreated)
	var ret returnPayload
	if err := json.NewDecoder(res.Body).Decode(&ret); err != nil {
		t.Fatalf("decode return: %v", err)
	}
	if ret.Status != "requested" || !ret.Restock || ret.OrderID != ord.ID {
		t.Fatalf("return should be requested/restock/order, got %+v", ret)
	}
	if len(ret.Items) != 1 || ret.Items[0].OrderItemID != itA.ID || ret.Items[0].Quantity != 2 {
		t.Fatalf("return should carry v1a ×2, got %+v", ret.Items)
	}

	// Returning more than ordered and fake order items are rejected.
	do("POST", "/api/v1/orders/"+ord.ID+"/returns?phone="+qPhone, a.id, "", "", `{
		"items":[{"order_item_id":"`+itA.ID+`","quantity":3}]}`,
		fiber.StatusBadRequest)
	do("POST", "/api/v1/orders/"+ord.ID+"/returns?phone="+qPhone, a.id, "", "", `{
		"items":[{"order_item_id":"00000000-0000-0000-0000-000000000000","quantity":1}]}`,
		fiber.StatusBadRequest)

	// Admin list shows it; tenant B's list stays empty; B cannot touch it.
	res = do("GET", "/api/v1/returns", a.id, "", a.token, "", fiber.StatusOK)
	var lA struct {
		Returns []returnPayload `json:"returns"`
	}
	if err := json.NewDecoder(res.Body).Decode(&lA); err != nil {
		t.Fatalf("decode returns a: %v", err)
	}
	if len(lA.Returns) != 1 || lA.Returns[0].ID != ret.ID {
		t.Fatalf("tenant A should list the return, got %+v", lA.Returns)
	}
	res = do("GET", "/api/v1/returns", b.id, "", b.token, "", fiber.StatusOK)
	var lB struct {
		Returns []returnPayload `json:"returns"`
	}
	if err := json.NewDecoder(res.Body).Decode(&lB); err != nil {
		t.Fatalf("decode returns b: %v", err)
	}
	if len(lB.Returns) != 0 {
		t.Fatalf("tenant B should see no returns, got %d", len(lB.Returns))
	}
	do("PATCH", "/api/v1/returns/"+ret.ID+"/status", b.id, "", b.token, `{"status":"received"}`,
		fiber.StatusNotFound)

	// A rejected return never restocks: file a second return for v1b, reject it.
	res = do("POST", "/api/v1/orders/"+ord.ID+"/returns?phone="+qPhone, a.id, "", "", `{
		"reason":"changed mind","items":[{"order_item_id":"`+itB.ID+`","quantity":1}]}`,
		fiber.StatusCreated)
	var ret2 returnPayload
	if err := json.NewDecoder(res.Body).Decode(&ret2); err != nil {
		t.Fatalf("decode return2: %v", err)
	}
	do("PATCH", "/api/v1/returns/"+ret2.ID+"/status", a.id, "", a.token, `{"status":"approved"}`,
		fiber.StatusOK)
	do("PATCH", "/api/v1/returns/"+ret2.ID+"/status", a.id, "", a.token, `{"status":"rejected"}`,
		fiber.StatusOK)
	if v := variantInv(t, ctx, pool, a.id, v1b); v != 4 {
		t.Fatalf("v1b must not restock on a rejected return, got %d", v)
	}

	// Walk the v1a return to received → exactly v1a's 2 units come back.
	do("PATCH", "/api/v1/returns/"+ret.ID+"/status", a.id, "", a.token, `{"status":"approved"}`,
		fiber.StatusOK)
	publicKey := "shopkeet:cache:product:" + a.id + "/" + p1 + "/public"
	before := rec.count(publicKey)
	res = do("PATCH", "/api/v1/returns/"+ret.ID+"/status", a.id, "", a.token, `{"status":"received"}`,
		fiber.StatusOK)
	var recv returnPayload
	if err := json.NewDecoder(res.Body).Decode(&recv); err != nil {
		t.Fatalf("decode received return: %v", err)
	}
	if recv.Status != "received" {
		t.Fatalf("return should be received, got %q", recv.Status)
	}
	if v := variantInv(t, ctx, pool, a.id, v1a); v != 8 {
		t.Fatalf("v1a should be back to 8 after received, got %d", v)
	}
	if v := variantInv(t, ctx, pool, a.id, v1b); v != 4 {
		t.Fatalf("v1b must stay 4 (only the returned variant restocks), got %d", v)
	}
	if key := "shopkeet:cache:product:" + a.id + "/" + p1 + "/public"; rec.count(key) != before+1 {
		t.Fatalf("restock should invalidate the cached product exactly once (got %d, before %d)",
			rec.count(key), before)
	}

	// received → refunded advances without a second restock.
	do("PATCH", "/api/v1/returns/"+ret.ID+"/status", a.id, "", a.token, `{"status":"refunded"}`,
		fiber.StatusOK)
	if v := variantInv(t, ctx, pool, a.id, v1a); v != 8 {
		t.Fatalf("v1a must not restock twice on refunded, got %d", v)
	}
	// No transition into received twice (rejected end state, invalid path).
	do("PATCH", "/api/v1/returns/"+ret2.ID+"/status", a.id, "", a.token, `{"status":"received"}`,
		fiber.StatusBadRequest)

	mu.Lock()
	rc := len(createdReturns)
	rr := len(restocked)
	mu.Unlock()
	if rc != 2 || rr != 1 {
		t.Fatalf("expected 2 return.created + 1 return.restocked, got %d/%d", rc, rr)
	}
}

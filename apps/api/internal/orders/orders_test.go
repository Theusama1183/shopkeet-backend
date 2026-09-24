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
)

func randSuffix5() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type orderPayload struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
	PaymentMethod string `json:"payment_method"`
	TotalCents    int    `json:"total_cents"`
	Currency      string `json:"currency"`
	Items         []struct {
		ProductID      string `json:"product_id"`
		Quantity       int    `json:"quantity"`
		UnitPriceCents int    `json:"unit_price_cents"`
	} `json:"items"`
}

type tenantO struct {
	id    string
	token string
}

// TestOrdersRLSIsolation is the Phase 5 acceptance criterion: a guest in
// merchant A's store completes COD checkout; inventory decrements exactly once;
// a concurrent-style second cart for the last unit is refused at checkout (not
// at add-to-cart time); the merchant lists the order and walks it to
// delivered+paid (emitting order.created / order.paid); tenant B sees nothing.
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

	seedProduct := func(tid, slug string, price, inv int, status string) string {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'usd', $5, $6) RETURNING id`,
			tid, slug, "ord-"+slug+"-"+sfx, price, inv, status).Scan(&id); err != nil {
			t.Fatalf("seed product %s: %v", slug, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return id
	}
	p1 := seedProduct(a.id, "p1", 1000, 3, "active")
	p2 := seedProduct(a.id, "p2", 250, 1, "active")

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

	app := fiber.New()
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

	// Two guests race for the same 3 units of p1 (2+2) plus p2 (1 of 1).
	s1 := "sess-one-" + sfx
	s2 := "sess-two-" + sfx
	do("POST", "/api/v1/cart", s1, `{"product_id":"`+p1+`","quantity":2}`, fiber.StatusOK)
	do("POST", "/api/v1/cart", s1, `{"product_id":"`+p2+`","quantity":1}`, fiber.StatusOK)
	do("POST", "/api/v1/cart", s2, `{"product_id":"`+p1+`","quantity":2}`, fiber.StatusOK)

	phone := "+1-555-" + sfx
	// Guest 1 completes COD checkout.
	co := do("POST", "/api/v1/checkout", s1,
		`{"customer_name":"Ada","customer_phone":"`+phone+`","customer_email":"ada@example.com","shipping_address":"1 Main St"}`,
		fiber.StatusCreated)
	var ord orderPayload
	if err := json.NewDecoder(co.Body).Decode(&ord); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if ord.Status != "pending" || ord.PaymentStatus != "pending" || ord.PaymentMethod != "cod" {
		t.Fatalf("new order should be pending/pending/cod, got %+v", ord)
	}
	if ord.TotalCents != 2250 || len(ord.Items) != 2 || ord.Currency != "usd" {
		t.Fatalf("order should total 2250 over 2 items, got %+v", ord)
	}

	// Inventory decremented exactly once.
	inv := func(prodID string) int {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", a.id)
		var n int
		if err := tx.QueryRow(ctx,
			"SELECT inventory_count FROM products WHERE id = $1", prodID).Scan(&n); err != nil {
			t.Fatalf("read inventory: %v", err)
		}
		return n
	}
	if v := inv(p1); v != 1 {
		t.Fatalf("p1 inventory should be 1 after checkout, got %d", v)
	}
	if v := inv(p2); v != 0 {
		t.Fatalf("p2 inventory should be 0 after checkout, got %d", v)
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

	// The second guest can no longer get p1 at checkout: 409 at checkout, not
	// at add-to-cart time.
	do("POST", "/api/v1/checkout", s2,
		`{"customer_name":"Grace","customer_phone":"+1-555-0000","shipping_address":"2 Main St"}`,
		fiber.StatusConflict)

	// order.created fired once for this order.
	mu.Lock()
	if len(created) != 1 || created[0] != ord.ID {
		t.Fatalf("expected one order.created for %s, got %v", ord.ID, created)
	}
	mu.Unlock()

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
	if len(list.Orders) != 1 || list.Orders[0].ID != ord.ID {
		t.Fatalf("admin sees exactly 1 order (ours), got %+v", list.Orders)
	}
	if len(list.Orders[0].Items) != 2 {
		t.Fatalf("admin order list missing items, got %+v", list.Orders[0])
	}
	res = adminGet("/api/v1/orders?status=pending", fiber.StatusOK)
	json.NewDecoder(res.Body).Decode(&list)
	if len(list.Orders) != 1 {
		t.Fatalf("filter status=pending should match 1, got %d", len(list.Orders))
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

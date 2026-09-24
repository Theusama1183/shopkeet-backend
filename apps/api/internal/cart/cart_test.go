package cart

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"
)

func randSuffix4() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type tenantC struct {
	id string
}

type item struct {
	ID             string `json:"id"`
	ProductID      string `json:"product_id"`
	Name           string `json:"name"`
	Slug           string `json:"slug"`
	Quantity       int    `json:"quantity"`
	PriceCents     int    `json:"price_cents"`
	Currency       string `json:"currency"`
	LineTotalCents int    `json:"line_total_cents"`
}

type cartResp struct {
	Cart *struct {
		ID         string `json:"id"`
		Items      []item `json:"items"`
		TotalCents int    `json:"total_cents"`
		Currency   string `json:"currency"`
	} `json:"cart"`
}

// TestCartRLSIsolation is the Phase 4 acceptance criterion. A guest session in
// merchant A's storefront builds a cart (merge + patch + delete, correct
// totals); a different session in A and A's session in B both start from a
// fresh, isolated cart; and the stock guard is NOT enforced at add-to-cart time
// (that arbitration is checkout's job, Phase 5) — even a 0-inventory product
// can be carted.
func TestCartRLSIsolation(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping integration")
	}

	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	defer pool.Close()

	sfx := randSuffix4()

	mkTenant := func(name string) tenantC {
		var tid string
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "cart-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		return tenantC{id: tid}
	}
	a := mkTenant("alpha")
	b := mkTenant("beta")

	// A's catalog: an in-stock active product, a 0-inventory active product
	// (stock must not gate adding), and an archived product (cannot be carted).
	seedProduct := func(tid, slug string, price int, inv int, status string) string {
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
			tid, slug, "cart-"+slug+"-"+sfx, price, inv, status).Scan(&id); err != nil {
			t.Fatalf("seed product %s: %v", slug, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return id
	}
	p1 := seedProduct(a.id, "p1", 1000, 5, "active") // in stock
	p2 := seedProduct(a.id, "p2", 250, 0, "active")  // zero stock — cartable now
	arch := seedProduct(a.id, "arch", 999, 1, "archived")

	app := fiber.New()
	RegisterRoutes(app.Group("/api/v1"), pool, New(pool, NoopReserver{}))

	type resp struct {
		httpResp *http.Response
		session  string
	}
	do := func(method, path, session, body string, want int) resp {
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
		return resp{
			httpResp: res,
			session:  res.Header.Get("X-Customer-Session"),
		}
	}
	decodeCart := func(r resp) cartResp {
		t.Helper()
		var cr cartResp
		if err := json.NewDecoder(r.httpResp.Body).Decode(&cr); err != nil {
			t.Fatalf("decode cart: %v", err)
		}
		return cr
	}
	find := func(items []item, productID string) *item {
		for i := range items {
			if items[i].ProductID == productID {
				return &items[i]
			}
		}
		return nil
	}

	sessionA := "sess-alpha-" + sfx

	// Fresh session: no cart yet.
	cr := decodeCart(do("GET", "/api/v1/cart", sessionA, "", fiber.StatusOK))
	if cr.Cart != nil {
		t.Fatalf("fresh session should have no cart, got %+v", cr.Cart)
	}

	// Add in-stock product, then merge more of it.
	cr = decodeCart(do("POST", "/api/v1/cart", sessionA,
		`{"product_id":"`+p1+`","quantity":2}`, fiber.StatusOK))
	if cr.Cart == nil || len(cr.Cart.Items) != 1 {
		t.Fatalf("expected 1 item after first add, got %+v", cr.Cart)
	}
	it := cr.Cart.Items[0]
	if it.ProductID != p1 || it.Quantity != 2 {
		t.Fatalf("p1 qty should be 2, got %+v", it)
	}
	if it.LineTotalCents != 2000 || cr.Cart.TotalCents != 2000 {
		t.Fatalf("totals wrong: line %d, total %d", it.LineTotalCents, cr.Cart.TotalCents)
	}

	cr = decodeCart(do("POST", "/api/v1/cart", sessionA,
		`{"product_id":"`+p1+`","quantity":3}`, fiber.StatusOK))
	if it = *find(cr.Cart.Items, p1); it.Quantity != 5 {
		t.Fatalf("adding same product should merge to 5, got %d", it.Quantity)
	}

	// 0-inventory product is cartable now (no stock gating at add time).
	cr = decodeCart(do("POST", "/api/v1/cart", sessionA,
		`{"product_id":"`+p2+`","quantity":1}`, fiber.StatusOK))
	if len(cr.Cart.Items) != 2 || cr.Cart.TotalCents != 5250 {
		t.Fatalf("expected 2 items / 5250 total after adding p2, got %+v", cr.Cart)
	}

	// Archived product cannot be carted.
	do("POST", "/api/v1/cart", sessionA,
		`{"product_id":"`+arch+`","quantity":1}`, fiber.StatusNotFound)

	// PATCH one line's quantity.
	p1Item := find(cr.Cart.Items, p1)
	cr = decodeCart(do("PATCH", "/api/v1/cart/items/"+p1Item.ID, sessionA,
		`{"quantity":7}`, fiber.StatusOK))
	if it = *find(cr.Cart.Items, p1); it.Quantity != 7 || cr.Cart.TotalCents != 7250 {
		t.Fatalf("patch to 7: line %d total %d, got %+v", it.Quantity, cr.Cart.TotalCents, cr.Cart)
	}

	// DELETE one line.
	cr = decodeCart(do("DELETE", "/api/v1/cart/items/"+find(cr.Cart.Items, p2).ID, sessionA, "",
		fiber.StatusOK))
	if len(cr.Cart.Items) != 1 || cr.Cart.TotalCents != 7000 {
		t.Fatalf("after delete: %d items / %d total, got %+v", len(cr.Cart.Items), cr.Cart.TotalCents, cr.Cart)
	}

	// --- Isolation -----------------------------------------------------------
	// A different session in the SAME tenant sees its own fresh cart.
	cr2 := decodeCart(do("GET", "/api/v1/cart", "sess-other-"+sfx, "", fiber.StatusOK))
	if cr2.Cart != nil {
		t.Fatalf("different session must have its own empty cart, got %+v", cr2.Cart)
	}
	// The same session in tenant B starts from nothing (RLS splits stores).
	reqB := httptest.NewRequest("GET", "/api/v1/cart", nil)
	reqB.Header.Set("X-Tenant-ID", b.id)
	reqB.Header.Set("X-Customer-Session", sessionA)
	resB, err := app.Test(reqB, -1)
	if err != nil {
		t.Fatalf("get cart in B: %v", err)
	}
	rawB, _ := io.ReadAll(resB.Body)
	resB.Body.Close()
	if resB.StatusCode != fiber.StatusOK {
		t.Fatalf("get cart in B: status %d body %s", resB.StatusCode, rawB)
	}
	var crB cartResp
	if err := json.Unmarshal(rawB, &crB); err != nil {
		t.Fatalf("decode B cart: %v", err)
	}
	if crB.Cart != nil {
		t.Fatalf("session A in tenant B must not see A's cart, got %+v", crB.Cart)
	}
	// Editing A's item id from tenant B is a 404, not a cross-tenant write.
	patchB := httptest.NewRequest("PATCH", "/api/v1/cart/items/"+p1Item.ID, strings.NewReader(`{"quantity":1}`))
	patchB.Header.Set("Content-Type", "application/json")
	patchB.Header.Set("X-Tenant-ID", b.id)
	patchB.Header.Set("X-Customer-Session", sessionA)
	resPatchB, err := app.Test(patchB, -1)
	if err != nil {
		t.Fatalf("patch from B: %v", err)
	}
	io.Copy(io.Discard, resPatchB.Body)
	resPatchB.Body.Close()
	if resPatchB.StatusCode != fiber.StatusNotFound {
		t.Fatalf("patching A's item from B should 404, got %d", resPatchB.StatusCode)
	}

	// --- Session minting without a pre-existing session ----------------------
	// A brand-new cart POST (no session header/cookie) mints one and echoes it.
	mint := do("POST", "/api/v1/cart", "", `{"product_id":"`+p1+`","quantity":1}`, fiber.StatusOK)
	if mint.session == "" {
		t.Fatal("expected a minted X-Customer-Session response header")
	}
	if cookie := mint.httpResp.Cookies(); len(cookie) != 1 || cookie[0].Name != "shopkeet_session" {
		t.Fatalf("expected shopkeet_session cookie set, got %+v", mint.httpResp.Cookies())
	}
	// The minted session now has a cart.
	crMint := decodeCart(do("GET", "/api/v1/cart", mint.session, "", fiber.StatusOK))
	if crMint.Cart == nil || len(crMint.Cart.Items) != 1 {
		t.Fatalf("minted session should have its cart, got %+v", crMint.Cart)
	}
	_ = a
}

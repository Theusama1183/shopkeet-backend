// External test package: it mounts cart (which imports bundles), so the test
// must live outside package bundles to avoid an import cycle.
package bundles_test

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
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/bundles"
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/orders"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
	"github.com/shopkeet/api/internal/shipping"
)

type cartResp struct {
	Cart *struct {
		TotalCents int `json:"total_cents"`
		Items      []struct {
			ID         string `json:"id"`
			Quantity   int    `json:"quantity"`
			LineTotal  int    `json:"line_total_cents"`
			BundleID   string `json:"bundle_id"`
		} `json:"items"`
	} `json:"cart"`
}

type orderResp struct {
	ID         string `json:"id"`
	TotalCents int    `json:"total_cents"`
	Items      []struct {
		Quantity int    `json:"quantity"`
		UnitPrice int   `json:"unit_price_cents"`
		BundleID string `json:"bundle_id"`
	} `json:"items"`
}

// TestBundlesAcceptance is the Phase 25 criterion: a fixed bundle in a cart
// prices at bundle_price_cents (not the sum of its component variants) both in
// the cart preview and at checkout; checkout still decrements each component's
// own stock and snapshots the real component prices into order_items; a
// mix_and_match bundle discounts the customer's whole pick; quantity breaks
// discount a plain line once its quantity qualifies; the admin CRUD + tenant
// isolation hold.
func TestBundlesAcceptance(t *testing.T) {
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
			name, "bun-"+name+"-"+sfx).Scan(&tid)
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
	bID, bToken := mkTenant("beta")

	seedProduct := func(tid string, name string, price, inv int) (prodID, variantID string) {
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
			tid, name, "bun-"+name+"-"+sfx, price, inv).Scan(&prodID); err != nil {
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

	aProd1, aV1 := seedProduct(aID, "alpha-one", 1000, 10)
	aProd2, aV2 := seedProduct(aID, "alpha-two", 500, 20)
	betaProd, _ := seedProduct(bID, "beta-one", 700, 5)

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
	rateID := seedZoneRate(aID)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	bundles.RegisterRoutes(v1, pool, secret, bundles.New(pool))
	bus := events.NewBus()
	orders.RegisterRoutes(v1, pool, secret, orders.New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))
	shipping.RegisterRoutes(v1, pool, secret, shipping.New(pool))

	client := func(tid, token string) func(method, path, session, body string, want int) *http.Response {
		return func(method, path, session, body string, want int) *http.Response {
			var req *http.Request
			if body == "" {
				req = httptest.NewRequest(method, path, nil)
			} else {
				req = httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("X-Tenant-ID", tid)
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
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
	admin := client(aID, aToken)
	cust := client(aID, "")
	betaAdmin := client(bID, bToken)

	mkBundle := func(body string, want int) string {
		res := admin("POST", "/api/v1/bundles", "", body, want)
		if res.StatusCode != fiber.StatusCreated {
			return ""
		}
		var b struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(res.Body).Decode(&b)
		return b.ID
	}

	// --- Admin CRUD + validation ---
	fixedID := mkBundle(`{"name":"Duo","type":"fixed","bundle_price_cents":900,"status":"active",
		"items":[{"product_id":"`+aProd1+`","quantity":1},{"product_id":"`+aProd2+`","quantity":1}]}`, fiber.StatusCreated)
	if fixedID == "" {
		t.Fatal("expected a bundle id")
	}
	// A bundle can only reference this tenant's products.
	mkBundle(`{"name":"Cross","type":"fixed","bundle_price_cents":900,
		"items":[{"product_id":"`+betaProd+`"}]}`, fiber.StatusBadRequest)
	// Exactly one of price / percent — both is rejected.
	mkBundle(`{"name":"Both","type":"fixed","bundle_price_cents":900,"discount_percent":10,
		"items":[{"product_id":"`+aProd1+`"}]}`, fiber.StatusBadRequest)
	// mix_and_match requires percent.
	mkBundle(`{"name":"Pick","type":"mix_and_match","bundle_price_cents":900,
		"items":[{"product_id":"`+aProd1+`"}]}`, fiber.StatusBadRequest)
	// fixed requires a flat price (a fixed bundle cannot carry the percent model).
	mkBundle(`{"name":"FixedPct","type":"fixed","discount_percent":10,
		"items":[{"product_id":"`+aProd1+`"}]}`, fiber.StatusBadRequest)
	// Omitting type defaults to fixed with a flat price.
	mkBundle(`{"name":"NoType","bundle_price_cents":700,
		"items":[{"product_id":"`+aProd1+`"}]}`, fiber.StatusCreated)
	mixID := mkBundle(`{"name":"PickMix","type":"mix_and_match","discount_percent":10,"status":"active",
		"items":[{"product_id":"`+aProd1+`"},{"product_id":"`+aProd2+`"}]}`, fiber.StatusCreated)
	_ = mixID
	// Draft bundles are hidden from the storefront / refused at add time.
	draftID := mkBundle(`{"name":"Drafty","type":"fixed","bundle_price_cents":800,
		"items":[{"product_id":"`+aProd1+`"}]}`, fiber.StatusCreated)
	_ = draftID

	// Public storefront lists only active bundles.
	res := cust("GET", "/api/v1/bundles", "", "", fiber.StatusOK)
	var pub struct {
		Bundles []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"bundles"`
	}
	if err := json.NewDecoder(res.Body).Decode(&pub); err != nil {
		t.Fatalf("decode public bundles: %v", err)
	}
	if len(pub.Bundles) != 2 {
		t.Fatalf("public list should show 2 active bundles, got %d", len(pub.Bundles))
	}
	for _, b := range pub.Bundles {
		if b.Status != "" {
			t.Fatalf("public bundle payload must not leak status, got %q", b.Status)
		}
	}

	// --- Fixed bundle: cart prices at 900, not the components' sum (1500) ---
	s1 := "sess-1-" + sfx
	res = cust("POST", "/api/v1/cart/bundle", s1, `{"bundle_id":"`+fixedID+`","quantity":1}`, fiber.StatusOK)
	var c cartResp
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		t.Fatalf("decode bundle cart: %v", err)
	}
	if c.Cart == nil || len(c.Cart.Items) != 2 {
		t.Fatalf("expected 2 component rows, got %+v", c.Cart)
	}
	if c.Cart.TotalCents != 900 {
		t.Fatalf("fixed bundle should cart at 900, got %d", c.Cart.TotalCents)
	}
	sum := 0
	allBundled := true
	for _, it := range c.Cart.Items {
		sum += it.LineTotal
		if it.BundleID != fixedID {
			allBundled = false
		}
	}
	if sum != 900 {
		t.Fatalf("bundle line totals should sum to 900, got %d", sum)
	}
	if !allBundled {
		t.Fatalf("every component row must carry the bundle_id")
	}

	checkout := func(session string, want int) *http.Response {
		return cust("POST", "/api/v1/checkout", session,
			`{"customer_name":"Ada","customer_phone":"+1-555-`+sfx+`",`+
				`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
				`"shipping_rate_id":"`+rateID+`"}`,
			want)
	}
	res = checkout(s1, fiber.StatusCreated)
	var o orderResp
	if err := json.NewDecoder(res.Body).Decode(&o); err != nil {
		t.Fatalf("decode order: %v", err)
	}
	if o.TotalCents != 1400 { // 900 goods + 500 shipping
		t.Fatalf("bundle checkout should total 1400, got %d", o.TotalCents)
	}
	if len(o.Items) != 2 {
		t.Fatalf("order should snapshot 2 component lines, got %d", len(o.Items))
	}
	for _, it := range o.Items {
		if it.BundleID != fixedID {
			t.Fatalf("order item should carry the bundle id, got %q", it.BundleID)
		}
	}

	// Stock decremented per component: alpha-one 10->9, alpha-two 20->19.
	verifyStock := func(variantID string, want int) {
		var got int
		if err := pool.QueryRow(ctx,
			"SELECT inventory_count FROM product_variants WHERE id = $1", variantID).Scan(&got); err != nil {
			t.Fatalf("stock read: %v", err)
		}
		if got != want {
			t.Fatalf("stock should be %d after checkout, got %d", want, got)
		}
	}
	verifyStock(aV1, 9)
	verifyStock(aV2, 19)

	// --- mix_and_match: the whole pick is discounted 10% (1500 -> 1350) ---
	s2 := "sess-2-" + sfx
	sel := `{"selections":[{"product_id":"` + aProd1 + `","quantity":1},{"product_id":"` + aProd2 + `","quantity":1}]}`
	res = cust("POST", "/api/v1/cart/bundle", s2, `{"bundle_id":"`+mixID+`",`+sel, fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		t.Fatalf("decode mix cart: %v", err)
	}
	if c.Cart.TotalCents != 1350 {
		t.Fatalf("mix_and_match should discount 10%% of 1500 -> 1350, got %d", c.Cart.TotalCents)
	}
	res = checkout(s2, fiber.StatusCreated)
	if err := json.NewDecoder(res.Body).Decode(&o); err != nil {
		t.Fatalf("decode mix order: %v", err)
	}
	if o.TotalCents != 1850 { // 1350 + 500
		t.Fatalf("mix checkout should total 1850, got %d", o.TotalCents)
	}

	// Draft bundle is refused at add time.
	s3 := "sess-3-" + sfx
	cust("POST", "/api/v1/cart/bundle", s3, `{"bundle_id":"`+draftID+`","quantity":1}`, fiber.StatusConflict)
	// Selecting something outside the mix pool is rejected.
	s3b := "sess-3b-" + sfx
	cust("POST", "/api/v1/cart/bundle", s3b,
		`{"bundle_id":"`+mixID+`","selections":[{"product_id":"`+aProd1+`"},{"product_id":"`+betaProd+`"}]}`,
		fiber.StatusBadRequest)

	// --- Quantity breaks ---
	admin("POST", "/api/v1/products/"+aProd2+"/quantity-breaks", "",
		`{"min_quantity":2,"discount_percent":10}`, fiber.StatusCreated)
	// Duplicate min_quantity for the same product -> 409.
	admin("POST", "/api/v1/products/"+aProd2+"/quantity-breaks", "",
		`{"min_quantity":2,"discount_percent":15}`, fiber.StatusConflict)
	res = admin("GET", "/api/v1/products/"+aProd2+"/quantity-breaks", "", "", fiber.StatusOK)
	var ql struct {
		Breaks []struct {
			MinQuantity     int `json:"min_quantity"`
			DiscountPercent int `json:"discount_percent"`
		} `json:"quantity_breaks"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ql); err != nil {
		t.Fatalf("decode breaks: %v", err)
	}
	if len(ql.Breaks) != 1 || ql.Breaks[0].MinQuantity != 2 || ql.Breaks[0].DiscountPercent != 10 {
		t.Fatalf("expected one 10%% break at min 2, got %+v", ql.Breaks)
	}

	// Two units of alpha-two (500 each) qualify the break -> 900 line, not 1000.
	s4 := "sess-4-" + sfx
	cust("POST", "/api/v1/cart", s4, `{"variant_id":"`+aV2+`","quantity":2}`, fiber.StatusOK)
	res = cust("GET", "/api/v1/cart", s4, "", fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&c); err != nil {
		t.Fatalf("decode break cart: %v", err)
	}
	if c.Cart.TotalCents != 900 {
		t.Fatalf("quantity break should discount line to 900, got %d", c.Cart.TotalCents)
	}
	res = checkout(s4, fiber.StatusCreated)
	if err := json.NewDecoder(res.Body).Decode(&o); err != nil {
		t.Fatalf("decode break order: %v", err)
	}
	if o.TotalCents != 1400 { // 900 + 500
		t.Fatalf("break checkout should total 1400, got %d", o.TotalCents)
	}

	// --- Tenant isolation ---
	betaAdmin("GET", "/api/v1/bundles/"+fixedID, "", "", fiber.StatusNotFound)
	admin("PATCH", "/api/v1/bundles/"+fixedID, "", `{"status":"archived"}`, fiber.StatusOK)
	s5 := "sess-5-" + sfx
	cust("POST", "/api/v1/cart/bundle", s5, `{"bundle_id":"`+fixedID+`","quantity":1}`, fiber.StatusConflict)
}
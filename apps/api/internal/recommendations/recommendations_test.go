// External test package: it mounts cart (which imports bundles) and orders,
// so the test must live outside package recommendations to avoid an import
// cycle.
package recommendations_test

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
	"github.com/shopkeet/api/internal/recommendations"
	"github.com/shopkeet/api/internal/shipping"
)

type recRow struct {
	ID          string `json:"id"`
	ProductID   string `json:"product_id"`
	Type        string `json:"type"`
	SortOrder   int    `json:"sort_order"`
	Status      string `json:"status"`
	Recommended struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Slug       string `json:"slug"`
		PriceCents int    `json:"price_cents"`
	} `json:"recommended_product"`
}

type orderResp struct {
	ID         string `json:"id"`
	TotalCents int    `json:"total_cents"`
	Items      []struct {
		VariantID string `json:"variant_id"`
		Quantity  int    `json:"quantity"`
		UnitPrice int    `json:"unit_price_cents"`
		LineTotal int    `json:"line_total_cents"`
		BundleID  string `json:"bundle_id"`
	} `json:"items"`
}

// TestRecommendationsAcceptance is the Phase 26 criterion: a merchant curates
// manual recommendations that surface on the public product endpoint (active
// products only, tenant-isolated, ordered); a customer can add a line to a
// still-pending order from the confirmation screen — stock is re-checked and
// decremented, total_cents is recalculated with checkout-accurate pricing
// (quantity breaks included) — and the add is refused once the order leaves
// the pending window.
func TestRecommendationsAcceptance(t *testing.T) {
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
			name, "rec-"+name+"-"+sfx).Scan(&tid)
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
			tid, name, "rec-"+name+"-"+sfx, price, inv).Scan(&prodID); err != nil {
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
	aProd3, _ := seedProduct(aID, "alpha-three", 700, 5)
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
	cart.RegisterRoutes(v1, pool, secret, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	bundles.RegisterRoutes(v1, pool, secret, bundles.New(pool))
	recommendations.RegisterRoutes(v1, pool, secret, recommendations.New(pool))
	bus := events.NewBus()
	orders.RegisterRoutes(v1, pool, secret, orders.New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))
	shipping.RegisterRoutes(v1, pool, secret, shipping.New(pool))

	client := func(tid, token string) func(method, path, body string, want int) *http.Response {
		return func(method, path, body string, want int) *http.Response {
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
	betaAdmin := client(bID, bToken)
	pub := client(aID, "")
	// Session-bearing customer client (order confirmation screen).
	cust := func(method, path, session, body string, want int) *http.Response {
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", aID)
		req.Header.Set("X-Customer-Session", session)
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

	mkRec := func(body string, want int) string {
		res := admin("POST", "/api/v1/products/"+aProd1+"/recommendations", body, want)
		if res.StatusCode != fiber.StatusCreated {
			return ""
		}
		var r struct {
			ID string `json:"id"`
		}
		_ = json.NewDecoder(res.Body).Decode(&r)
		return r.ID
	}

	// --- Admin manual curation + validation ---
	rid1 := mkRec(`{"recommended_product_id":"`+aProd2+`","sort_order":1}`, fiber.StatusCreated)
	if rid1 == "" {
		t.Fatal("expected a recommendation id")
	}
	rid2 := mkRec(`{"recommended_product_id":"`+aProd3+`","sort_order":0}`, fiber.StatusCreated)
	if rid2 == "" {
		t.Fatal("expected a second recommendation id")
	}
	// Duplicate (product, recommended, type) -> 409.
	mkRec(`{"recommended_product_id":"`+aProd2+`"}`, fiber.StatusConflict)
	// A product cannot recommend itself.
	mkRec(`{"recommended_product_id":"`+aProd1+`"}`, fiber.StatusBadRequest)
	// Unknown type -> 400.
	mkRec(`{"recommended_product_id":"`+aProd2+`","type":"magic"}`, fiber.StatusBadRequest)
	// Negative sort_order -> 400.
	mkRec(`{"recommended_product_id":"`+aProd2+`","sort_order":-1}`, fiber.StatusBadRequest)
	// Cross-tenant curation -> 400 (beta's product is invisible to alpha's RLS).
	mkRec(`{"recommended_product_id":"`+betaProd+`"}`, fiber.StatusBadRequest)
	// A beta admin cannot curate alpha's product.
	betaAdmin("POST", "/api/v1/products/"+aProd1+"/recommendations",
		`{"recommended_product_id":"`+aProd2+`"}`, fiber.StatusBadRequest)

	// --- Public list: ordered, pinned to active products, status never leaks ---
	res := pub("GET", "/api/v1/products/"+aProd1+"/recommendations", "", fiber.StatusOK)
	var public struct {
		Recommendations []recRow `json:"recommendations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&public); err != nil {
		t.Fatalf("decode public recs: %v", err)
	}
	if len(public.Recommendations) != 2 {
		t.Fatalf("public list should show 2 picks, got %d", len(public.Recommendations))
	}
	if public.Recommendations[0].Recommended.Name != "alpha-three" || public.Recommendations[1].Recommended.Name != "alpha-two" {
		t.Fatalf("picks should be sort-ordered (alpha-three, alpha-two), got %+v", public.Recommendations)
	}
	for _, r := range public.Recommendations {
		if r.Status != "" {
			t.Fatalf("public payload must not leak status, got %q", r.Status)
		}
	}

	// Archiving the recommended product hides it from the public list but not
	// the admin list.
	// A direct pool write evaluated a request out of a transaction sees the
	// reset-to-'' custom GUC, so run it inside a tenant-scoped transaction.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin archive tx: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", aID); err != nil {
		t.Fatalf("set archive tenant: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"UPDATE products SET status = 'archived' WHERE id = $1", aProd3); err != nil {
		t.Fatalf("archive aProd3: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit archive tx: %v", err)
	}
	res = pub("GET", "/api/v1/products/"+aProd1+"/recommendations", "", fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&public); err != nil {
		t.Fatalf("decode public recs: %v", err)
	}
	if len(public.Recommendations) != 1 || public.Recommendations[0].Recommended.Name != "alpha-two" {
		t.Fatalf("archived picks must drop from public list, got %+v", public.Recommendations)
	}
	res = admin("GET", "/api/v1/products/"+aProd1+"/recommendations", "", fiber.StatusOK)
	var adminList struct {
		Recommendations []recRow `json:"recommendations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&adminList); err != nil {
		t.Fatalf("decode admin recs: %v", err)
	}
	if len(adminList.Recommendations) != 2 {
		t.Fatalf("admin list should still show the archived pick, got %d", len(adminList.Recommendations))
	}
	archivedShown := false
	for _, r := range adminList.Recommendations {
		if r.Recommended.Name == "alpha-three" && r.Status == "archived" {
			archivedShown = true
		}
	}
	if !archivedShown {
		t.Fatalf("admin list should surface the archived pick with its status, got %+v", adminList.Recommendations)
	}

	// A beta tenant cannot read alpha's recommendations (404: product absent).
	betaAdmin("GET", "/api/v1/products/"+aProd1+"/recommendations", "", fiber.StatusNotFound)

	// --- Post-purchase upsell while pending ---
	// alpha-two gets a 2+ quantity break at 10%; an add of 2 units later must
	// price at 900, exactly like the cart.
	admin("POST", "/api/v1/products/"+aProd2+"/quantity-breaks", `{"min_quantity":2,"discount_percent":10}`, fiber.StatusCreated)

	phone := "+1-555-" + sfx
	s := "sess-add-" + sfx
	cust("POST", "/api/v1/cart", s, `{"variant_id":"`+aV1+`","quantity":1}`, fiber.StatusOK)
	res = cust("POST", "/api/v1/checkout", s,
		`{"customer_name":"Ada","customer_phone":"`+phone+`",`+
			`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+rateID+`"}`,
		fiber.StatusCreated)
	var o orderResp
	if err := json.NewDecoder(res.Body).Decode(&o); err != nil {
		t.Fatalf("decode checkout: %v", err)
	}
	oid := o.ID
	if o.TotalCents != 1500 { // 1000 goods + 500 shipping
		t.Fatalf("checkout should total 1500, got %d", o.TotalCents)
	}
	verifyStock := func(variantID string, want int) {
		var got int
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin stock tx: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", aID); err != nil {
			t.Fatalf("set stock tenant: %v", err)
		}
		if err := tx.QueryRow(ctx,
			"SELECT inventory_count FROM product_variants WHERE id = $1", variantID).Scan(&got); err != nil {
			t.Fatalf("stock read: %v", err)
		}
		if got != want {
			t.Fatalf("stock should be %d, got %d", want, got)
		}
	}
	verifyStock(aV1, 9) // 10 - 1 check-out

	// Privacy: a wrong phone cannot see or touch the order.
	cust("POST", "/api/v1/orders/"+oid+"/add-item", "sess-other-"+sfx,
		`{"customer_phone":"+9-000-000","variant_id":"`+aV2+`","quantity":1}`, fiber.StatusNotFound)

	// The pending-window add: 2x alpha-two (500 each) triggers the 10% break ->
	// 900 delta; total 1500 -> 2400; stock 20 -> 18.
	res = cust("POST", "/api/v1/orders/"+oid+"/add-item", s,
		`{"customer_phone":"`+phone+`","variant_id":"`+aV2+`","quantity":2}`, fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&o); err != nil {
		t.Fatalf("decode add-item: %v", err)
	}
	if o.TotalCents != 2400 {
		t.Fatalf("add-item should raise total to 2400 (1500 + 900), got %d", o.TotalCents)
	}
	if len(o.Items) != 2 {
		t.Fatalf("order should now have 2 lines, got %d", len(o.Items))
	}
	addedIdx := -1
	for i := range o.Items {
		if o.Items[i].VariantID == aV2 {
			addedIdx = i
		}
	}
	if addedIdx == -1 {
		t.Fatalf("order should contain the added alpha-two line, got %+v", o.Items)
	}
	if o.Items[addedIdx].Quantity != 2 || o.Items[addedIdx].UnitPrice != 500 {
		t.Fatalf("added line should snapshot qty=2 at 500, got %+v", o.Items[addedIdx])
	}
	verifyStock(aV1, 9)  // unchanged by the add
	verifyStock(aV2, 18) // 20 - 2

	// Insufficient stock is refused inside the pending window.
	cust("POST", "/api/v1/orders/"+oid+"/add-item", s,
		`{"customer_phone":"`+phone+`","variant_id":"`+aV1+`","quantity":10}`, fiber.StatusConflict)
	// Quantity bounds.
	cust("POST", "/api/v1/orders/"+oid+"/add-item", s,
		`{"customer_phone":"`+phone+`","variant_id":"`+aV1+`","quantity":0}`, fiber.StatusBadRequest)

	// Moving the order to confirmed closes the window.
	admin("PATCH", "/api/v1/orders/"+oid+"/status", `{"status":"confirmed"}`, fiber.StatusOK)
	cust("POST", "/api/v1/orders/"+oid+"/add-item", s,
		`{"customer_phone":"`+phone+`","variant_id":"`+aV1+`","quantity":1}`, fiber.StatusConflict)

	// --- Admin delete ---
	admin("DELETE", "/api/v1/products/"+aProd1+"/recommendations/"+rid2, "", fiber.StatusOK)
	admin("DELETE", "/api/v1/products/"+aProd1+"/recommendations/"+rid2, "", fiber.StatusNotFound)
	// Cross-tenant delete cannot touch alpha's picks.
	betaAdmin("DELETE", "/api/v1/products/"+aProd1+"/recommendations/"+rid1, "", fiber.StatusNotFound)
}

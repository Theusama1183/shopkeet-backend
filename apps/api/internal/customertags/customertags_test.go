// External test package (mirrors discounts/customers wiring): Phase 33
// acceptance — customer tags & segments, and the delivery gateway that gates
// discount codes on a tag. It mounts cart + orders because the eligibility check
// runs at checkout, so the test must live outside package customertags.
package customertags_test

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
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/customertags"
	"github.com/shopkeet/api/internal/discounts"
	"github.com/shopkeet/api/internal/orders"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
	"github.com/shopkeet/api/internal/shipping"
)

// TestCustomerTagsAndSegments is the Phase 33 criterion: a merchant tags a
// customer, lists the customer (optionally narrowed by tag), and gates discount
// codes on an eligible_tag — a guest or an untagged customer is refused, a
// tagged customer gets the discount, and removing the tag before checkout
// invalidates an already-applied code. Automatic (requires_code=false) promos
// that are tag-gated simply stop matching for shoppers without the tag.
func TestCustomerTagsAndSegments(t *testing.T) {
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
			name, "tag33-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tid, token
	}
	aid, aToken := mkTenant("alpha")
	bid, bToken := mkTenant("beta")

	seedProduct := func(tid string, price, inv int) (variantID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin product tx: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var prodID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
			VALUES ($1, 'Tag Mug', $2, $3, 'usd', 30, 'active') RETURNING id`,
			tid, "tag33-mug-"+sfx, price).Scan(&prodID); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
			tid, prodID, price, inv).Scan(&variantID); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit product tx: %v", err)
		}
		return variantID
	}
	v := seedProduct(aid, 2000, 30)

	seedCustomer := func(tid string, email string) (cid string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin customer tx: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set customer tenant: %v", err)
		}
		if err := tx.QueryRow(ctx,
			"INSERT INTO customers (tenant_id, email) VALUES ($1, $2) RETURNING id",
			tid, email).Scan(&cid); err != nil {
			t.Fatalf("seed customer: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit customer tx: %v", err)
		}
		return cid
	}
	cid1 := seedCustomer(aid, "vip-tag33-"+sfx+"@example.io")
	cid2 := seedCustomer(aid, "plain-tag33-"+sfx+"@example.io")
	betaCust := seedCustomer(bid, "beta-tag33-"+sfx+"@example.io")
	c1Token, err := auth.SignCustomer(secret, aid, cid1, time.Hour)
	if err != nil {
		t.Fatalf("sign c1: %v", err)
	}
	c2Token, err := auth.SignCustomer(secret, aid, cid2, time.Hour)
	if err != nil {
		t.Fatalf("sign c2: %v", err)
	}

	seedZoneRate := func(tid string) (rateID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin zone tx: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set zone tenant: %v", err)
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
			t.Fatalf("commit zone tx: %v", err)
		}
		return rateID
	}
	rateID := seedZoneRate(aid)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, secret, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	discounts.RegisterRoutes(v1, pool, secret, discounts.New(pool))
	bus := events.NewBus()
	orders.RegisterRoutes(v1, pool, secret, orders.New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))
	shipping.RegisterRoutes(v1, pool, secret, shipping.New(pool))
	customertags.RegisterRoutes(v1, customertags.New(pool), pool, secret)

	do := func(tid, bearer, session string) func(method, path, body string, want int) *http.Response {
		return func(method, path, body string, want int) *http.Response {
			var r *http.Request
			if body == "" {
				r = httptest.NewRequest(method, path, nil)
			} else {
				r = httptest.NewRequest(method, path, strings.NewReader(body))
				r.Header.Set("Content-Type", "application/json")
			}
			r.Header.Set("X-Tenant-ID", tid)
			if bearer != "" {
				r.Header.Set("Authorization", "Bearer "+bearer)
			}
			if session != "" {
				r.Header.Set("X-Customer-Session", session)
			}
			res, err := app.Test(r, -1)
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
	admin := do(aid, aToken, "")
	c1 := func(session string) func(method, path, body string, want int) *http.Response {
		return do(aid, c1Token, session)
	}
	c2 := func(session string) func(method, path, body string, want int) *http.Response {
		return do(aid, c2Token, session)
	}
	guest := func(session string) func(method, path, body string, want int) *http.Response {
		return do(aid, "", session)
	}

	listCustomers := func(client func(method, path, body string, want int) *http.Response, query string) map[string][]string {
		res := client("GET", "/api/v1/customers"+query, "", fiber.StatusOK)
		var body struct {
			Customers []struct {
				ID    string   `json:"id"`
				Email string   `json:"email"`
				Tags  []string `json:"tags"`
			} `json:"customers"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("decode customers: %v", err)
		}
		out := map[string][]string{}
		for _, cu := range body.Customers {
			out[cu.ID] = cu.Tags
		}
		return out
	}

	// --- Admin tagging CRUD + segment listing ---
	admin("POST", "/api/v1/customers/"+cid1+"/tags", `{"tag":"VIP"}`, fiber.StatusCreated)
	res := admin("POST", "/api/v1/customers/"+cid1+"/tags", `{"tag":"vip"}`, fiber.StatusCreated)
	var tagBody struct {
		Tag string `json:"tag"`
	}
	if err := json.NewDecoder(res.Body).Decode(&tagBody); err != nil {
		t.Fatalf("decode tag: %v", err)
	}
	if tagBody.Tag != "vip" {
		t.Fatalf("tag should normalize to lowercase vip, got %q", tagBody.Tag)
	}
	admin("POST", "/api/v1/customers/"+cid1+"/tags", `{"tag":"bulk order"}`, fiber.StatusBadRequest)
	admin("POST", "/api/v1/customers/"+cid1+"/tags", `{"tag":""}`, fiber.StatusBadRequest)
	// Cross-tenant id is invisible (RLS scope) -> 404.
	admin("POST", "/api/v1/customers/"+betaCust+"/tags", `{"tag":"vip"}`, fiber.StatusNotFound)

	tags := listCustomers(admin, "")
	if len(tags) != 2 {
		t.Fatalf("alpha should list 2 customers, got %d", len(tags))
	}
	if act, ok := tags[cid1]; !ok || len(act) != 1 || act[0] != "vip" {
		t.Fatalf("cid1 should carry ['vip'], got %v", act)
	}
	if act, ok := tags[cid2]; !ok || len(act) != 0 {
		t.Fatalf("cid2 should carry no tags, got %v", act)
	}
	if seg := listCustomers(admin, "?tag=vip"); len(seg) != 1 || seg[cid1] == nil {
		t.Fatalf("?tag=vip should isolate cid1, got %v", seg)
	}
	if seg := listCustomers(admin, "?tag=wholesale"); len(seg) != 0 {
		t.Fatalf("?tag=wholesale should be empty, got %v", seg)
	}
	// Tenant isolation: beta's admin sees no alpha tags/customers.
	if seg := listCustomers(do(bid, bToken, ""), "?tag=vip"); len(seg) != 0 {
		t.Fatalf("beta must not see alpha's tags, got %v", seg)
	}

	// --- Discount gated on eligible_tag ---
	res = admin("POST", "/api/v1/discounts",
		`{"code":"VIPX","type":"percentage","value_percent":10,"eligible_tag":"vip"}`, fiber.StatusCreated)
	var disc struct {
		EligibleTag *string `json:"eligible_tag"`
	}
	if err := json.NewDecoder(res.Body).Decode(&disc); err != nil {
		t.Fatalf("decode discount: %v", err)
	}
	if disc.EligibleTag == nil || *disc.EligibleTag != "vip" {
		t.Fatalf("discount should carry eligible_tag=vip, got %v", disc.EligibleTag)
	}

	// Guest cannot apply a tag-gated code (no identity to verify).
	sG := "sess-guest-" + sfx
	guest(sG)("POST", "/api/v1/cart", `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	guest(sG)("POST", "/api/v1/cart/discount", `{"code":"VIPX"}`, fiber.StatusBadRequest)

	// Tagged customer: applies and checks out discounted.
	s1 := "sess-c1-" + sfx
	c1(s1)("POST", "/api/v1/cart", `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	res = c1(s1)("POST", "/api/v1/cart/discount", `{"code":"VIPX"}`, fiber.StatusOK)
	var cartRes struct {
		Cart *struct {
			DiscountCode  string `json:"discount_code"`
			DiscountCents int    `json:"discount_cents"`
		} `json:"cart"`
	}
	if err := json.NewDecoder(res.Body).Decode(&cartRes); err != nil {
		t.Fatalf("decode cart: %v", err)
	}
	if cartRes.Cart == nil || cartRes.Cart.DiscountCode != "VIPX" || cartRes.Cart.DiscountCents != 200 {
		t.Fatalf("tagged cart should show VIPX/200, got %+v", cartRes.Cart)
	}
	res = c1(s1)("POST", "/api/v1/checkout", `{"customer_name":"Ada","customer_phone":"+1-555-`+sfx+`",`+
		`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
		`"shipping_rate_id":"`+rateID+`"}`, fiber.StatusCreated)
	var ord struct {
		TotalCents    int    `json:"total_cents"`
		DiscountCents int    `json:"discount_cents"`
		DiscountCode  string `json:"discount_code"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ord); err != nil {
		t.Fatalf("decode tagged checkout: %v", err)
	}
	if ord.DiscountCode != "VIPX" || ord.DiscountCents != 200 || ord.TotalCents != 2300 {
		t.Fatalf("tagged checkout should be VIPX/200/2300, got code=%q cents=%d total=%d",
			ord.DiscountCode, ord.DiscountCents, ord.TotalCents)
	}

	// Remove the tag: the segment empties, a fresh cart can't re-apply the code,
	// and the untagged cid2 can never apply it.
	admin("DELETE", "/api/v1/customers/"+cid1+"/tags?tag=vip", "", fiber.StatusOK)
	if seg := listCustomers(admin, "?tag=vip"); len(seg) != 0 {
		t.Fatalf("removed vip tag should leave the segment empty, got %v", seg)
	}
	s3 := "sess-c3-" + sfx
	c1(s3)("POST", "/api/v1/cart", `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	c1(s3)("POST", "/api/v1/cart/discount", `{"code":"VIPX"}`, fiber.StatusBadRequest)
	s2 := "sess-c2-" + sfx
	c2(s2)("POST", "/api/v1/cart", `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	c2(s2)("POST", "/api/v1/cart/discount", `{"code":"VIPX"}`, fiber.StatusBadRequest)

	// Tag removed AFTER applying still refuses at checkout (authoritative).
	admin("POST", "/api/v1/customers/"+cid1+"/tags", `{"tag":"vip"}`, fiber.StatusCreated)
	s4 := "sess-c4-" + sfx
	c1(s4)("POST", "/api/v1/cart", `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	c1(s4)("POST", "/api/v1/cart/discount", `{"code":"VIPX"}`, fiber.StatusOK)
	admin("DELETE", "/api/v1/customers/"+cid1+"/tags?tag=vip", "", fiber.StatusOK)
	c1(s4)("POST", "/api/v1/checkout", `{"customer_name":"Ada","customer_phone":"+1-555-`+sfx+`",`+
		`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
		`"shipping_rate_id":"`+rateID+`"}`, fiber.StatusBadRequest)

	// --- Tag-gated automatic (requires_code=false) discount ---
	// AutoPick must skip it for untagged shoppers and apply it for tagged ones.
	admin("POST", "/api/v1/discounts",
		`{"code":"AUTO","type":"percentage","value_percent":5,"requires_code":false,"applies_to":"order","eligible_tag":"vip"}`,
		fiber.StatusCreated)
	s5 := "sess-c5-" + sfx
	c2(s5)("POST", "/api/v1/cart", `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	res = c2(s5)("POST", "/api/v1/checkout", `{"customer_name":"Ada","customer_phone":"+1-555-`+sfx+`",`+
		`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
		`"shipping_rate_id":"`+rateID+`"}`, fiber.StatusCreated)
	if err := json.NewDecoder(res.Body).Decode(&ord); err != nil {
		t.Fatalf("decode untagged order: %v", err)
	}
	if ord.TotalCents != 2500 || ord.DiscountCents != 0 {
		t.Fatalf("untagged customer should pay full 2500, got total=%d disc=%d", ord.TotalCents, ord.DiscountCents)
	}
	// Re-tag cid1 so the auto promo applies for the tagged shopper.
	admin("POST", "/api/v1/customers/"+cid1+"/tags", `{"tag":"vip"}`, fiber.StatusCreated)
	s6 := "sess-c6-" + sfx
	c1(s6)("POST", "/api/v1/cart", `{"variant_id":"`+v+`","quantity":1}`, fiber.StatusOK)
	res = c1(s6)("POST", "/api/v1/checkout", `{"customer_name":"Ada","customer_phone":"+1-555-`+sfx+`",`+
		`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
		`"shipping_rate_id":"`+rateID+`"}`, fiber.StatusCreated)
	if err := json.NewDecoder(res.Body).Decode(&ord); err != nil {
		t.Fatalf("decode tagged order: %v", err)
	}
	if ord.TotalCents != 2400 || ord.DiscountCents != 100 {
		t.Fatalf("tagged customer should get the 5%% auto (2400 total), got total=%d disc=%d", ord.TotalCents, ord.DiscountCents)
	}
	if ord.DiscountCode != "" {
		t.Fatalf("auto discount must not snapshot a code, got %q", ord.DiscountCode)
	}
}
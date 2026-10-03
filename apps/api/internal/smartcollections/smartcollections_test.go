// External test package: exercises Phase 31 smart (rule-based) collections end
// to end — a category declared smart with rule `price < 2000` auto-adopts a
// new product priced 1500, drops it when the price crosses the threshold,
// flips when rules change, survives manual re-category edits, is fed by the
// bulk CSV importer, and never crosses tenants. Runs against a real database
// (skipped unless DATABASE_URL is set), mirroring the affiliates/loyalty/
// metafields acceptance suites.
package smartcollections_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
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
	"github.com/shopkeet/api/internal/bulkcsv"
	"github.com/shopkeet/api/internal/catalog"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/smartcollections"
)

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

type ruleJSON struct {
	Field    string `json:"field"`
	Operator string `json:"operator"`
	Value    string `json:"value"`
}

type catResp struct {
	ID      string     `json:"id"`
	Name    string     `json:"name"`
	Slug    string     `json:"slug"`
	IsSmart bool       `json:"is_smart"`
	Rules   []ruleJSON `json:"rules"`
}

func decodeCat(t *testing.T, body string) catResp {
	t.Helper()
	var c catResp
	if err := json.NewDecoder(strings.NewReader(body)).Decode(&c); err != nil {
		t.Fatalf("decode category: %v (body=%s)", err, body)
	}
	return c
}

func decodeJSON(t *testing.T, body string, v any) {
	t.Helper()
	if err := json.NewDecoder(strings.NewReader(body)).Decode(v); err != nil {
		t.Fatalf("decode json: %v (body=%s)", err, body)
	}
}

func TestSmartCollections(t *testing.T) {
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

	const secret = "test-secret-31"
	sfx := hex.EncodeToString(func() []byte {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		return b
	}())[:8]

	mkTenant := func(name string) (tid, adminToken string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "sc31-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		adminToken, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tid, adminToken
	}
	aID, aAdmin := mkTenant("alpha")
	bID, bAdmin := mkTenant("beta")

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	scSvc := smartcollections.New(pool)
	smartcollections.RegisterRoutes(v1, scSvc, pool, secret)
	catalog.RegisterRoutes(v1, pool, secret, catalog.New(pool))

	doTenant := func(tid, adminToken string) func(method, path, body string, want int) *http.Response {
		return func(method, path, body string, want int) *http.Response {
			var req *http.Request
			if body == "" {
				req = httptest.NewRequest(method, path, nil)
			} else {
				req = httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("X-Tenant-ID", tid)
			if adminToken != "" {
				req.Header.Set("Authorization", "Bearer "+adminToken)
			}
			res, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if res.StatusCode != want {
				t.Fatalf("%s %s: status %d, want %d (body=%s)", method, path,
					res.StatusCode, want, readBody(t, res))
			}
			return res
		}
	}
	admin := doTenant(aID, aAdmin)
	pub := doTenant(aID, "")
	adminB := doTenant(bID, bAdmin)

	// --- Smart collection + a manual control category -------------------------
	res := admin("POST", "/api/v1/categories",
		`{"name":"Under $20","slug":"under-20","is_smart":true,"rules":[{"field":"price","operator":"lt","value":"2000"}]}`,
		fiber.StatusCreated)
	under := decodeCat(t, readBody(t, res))
	if !under.IsSmart || len(under.Rules) != 1 ||
		under.Rules[0].Field != "price" || under.Rules[0].Value != "2000" {
		t.Fatalf("smart category mismatch: %+v", under)
	}

	res = admin("POST", "/api/v1/categories", `{"name":"T-Shirts","slug":"tshirts"}`, fiber.StatusCreated)
	manual := decodeCat(t, readBody(t, res))
	if manual.IsSmart || len(manual.Rules) != 0 || manual.Slug != "tshirts" {
		t.Fatalf("manual category mismatch: %+v", manual)
	}

	// Auto slug when omitted (Phase 3 behaviour retained).
	res = admin("POST", "/api/v1/categories", `{"name":"Sale Items"}`, fiber.StatusCreated)
	if s := decodeCat(t, readBody(t, res)); s.Slug != "sale-items" {
		t.Fatalf("derived slug = %q, want sale-items", s.Slug)
	}

	invalid := map[string]string{
		`{"name":"Bad","is_smart":true,"rules":[{"field":"color","operator":"eq","value":"red"}]}`:     "invalid field",
		`{"name":"Bad","is_smart":true,"rules":[{"field":"price","operator":"contains","value":"2"}]}`: "contains not allowed",
		`{"name":"Bad","is_smart":true,"rules":[{"field":"price","operator":"lt","value":"abc"}]}`:     "integer",
		`{"name":"Bad","is_smart":true,"rules":[{"field":"status","operator":"eq","value":"frozen"}]}`: "draft|active|archived",
		`{"name":"Bad","is_smart":true,"rules":[{"field":"name","operator":"like","value":"x"}]}`:      "invalid operator",
	}
	for body, wantMsg := range invalid {
		res := admin("POST", "/api/v1/categories", body, fiber.StatusBadRequest)
		if !strings.Contains(readBody(t, res), wantMsg) {
			t.Fatalf("body=%s: want message containing %q, got %s", body, wantMsg, readBody(t, res))
		}
	}
	admin("POST", "/api/v1/categories", `{"name":"Dup","slug":"dupy"}`, fiber.StatusCreated)
	admin("POST", "/api/v1/categories", `{"name":"Dup2","slug":"dupy"}`, fiber.StatusConflict)

	// --- Product creation auto-membership (spec acceptance) -------------------
	slug := func(s string) string { return s + "-" + sfx }
	createProd := func(name string, price int, cats string) string {
		body := fmt.Sprintf(`{"name":%q,"slug":%q,"price_cents":%d,"status":"active"`, name, slug(slugify(name)), price)
		if cats != "" {
			body += `,"category_ids":[` + cats + `]`
		}
		body += `}`
		res := admin("POST", "/api/v1/products", body, fiber.StatusCreated)
		var p struct {
			ID string `json:"id"`
		}
		decodeJSON(t, readBody(t, res), &p)
		return p.ID
	}
	cheap := createProd("cheap tee", 1500, "")   // matches price < 2000
	pricey := createProd("pricey tee", 5000, "") // does not
	mixed := createProd("mixed tee", 1500, `"`+manual.ID+`"`)

	productsIn := func(catSlug string) map[string]bool {
		res := pub("GET", "/api/v1/products?category="+catSlug, "", fiber.StatusOK)
		var out struct {
			Products []struct {
				ID string `json:"id"`
			} `json:"products"`
		}
		decodeJSON(t, readBody(t, res), &out)
		m := map[string]bool{}
		for _, p := range out.Products {
			m[p.ID] = true
		}
		return m
	}

	got := productsIn("under-20")
	if !got[cheap] {
		t.Fatalf("product %s (1500) missing from smart 'under-20'", cheap[:8])
	}
	if got[pricey] {
		t.Fatalf("product %s (5000) wrongly in smart 'under-20'", pricey[:8])
	}
	if !got[mixed] {
		t.Fatalf("product %s (1500, also manual-linked) missing from smart category", mixed[:8])
	}
	// Manual membership survived alongside the derived one.
	if m := productsIn("tshirts"); !m[mixed] {
		t.Fatalf("product %s missing from manual 'tshirts'", mixed[:8])
	}

	// Product detail embeds is_smart on its categories.
	res = pub("GET", "/api/v1/products/"+cheap, "", fiber.StatusOK)
	var det struct {
		Categories []struct {
			ID      string `json:"id"`
			IsSmart bool   `json:"is_smart"`
		} `json:"categories"`
	}
	decodeJSON(t, readBody(t, res), &det)
	var sawSmart bool
	for _, c := range det.Categories {
		if c.ID == under.ID && !c.IsSmart {
			t.Fatalf("smart category flagged is_smart=false on product detail")
		}
		if c.IsSmart {
			sawSmart = true
		}
	}
	if !sawSmart {
		t.Fatalf("product detail categories missing the smart category: %+v", det.Categories)
	}

	// --- Price crossing the threshold removes, then re-adds -------------------
	admin("PATCH", "/api/v1/products/"+cheap, `{"price_cents":2500}`, fiber.StatusOK)
	if got = productsIn("under-20"); got[cheap] {
		t.Fatalf("product %s still in 'under-20' after price rose to 2500", cheap[:8])
	}
	admin("PATCH", "/api/v1/products/"+cheap, `{"price_cents":1500}`, fiber.StatusOK)
	if got = productsIn("under-20"); !got[cheap] {
		t.Fatalf("product %s not back in 'under-20' after price fell to 1500", cheap[:8])
	}

	// --- Rule change recomputes existing membership ---------------------------
	admin("PATCH", "/api/v1/categories/"+under.ID,
		`{"rules":[{"field":"price","operator":"lt","value":"6000"}]}`, fiber.StatusOK)
	if got = productsIn("under-20"); !got[cheap] || !got[pricey] {
		t.Fatalf("rule change to <6000 not applied: cheap=%v pricey=%v", got[cheap], got[pricey])
	}
	// A product riding exactly the boundary stays out.
	admin("PATCH", "/api/v1/categories/"+under.ID,
		`{"rules":[{"field":"price","operator":"lt","value":"5000"}]}`, fiber.StatusOK)
	if got = productsIn("under-20"); got[pricey] {
		t.Fatalf("pricey (5000) should not match strict <5000 (still in: %+v)", got)
	}

	// Rule removal resets to ALL products matching (no rules left -> all match).
	admin("PATCH", "/api/v1/categories/"+under.ID, `{"rules":[]}`, fiber.StatusOK)
	got = productsIn("under-20")
	if !got[cheap] || !got[pricey] || !got[mixed] {
		t.Fatalf("empty rules should match everything: %+v", got)
	}
	admin("PATCH", "/api/v1/categories/"+under.ID,
		`{"rules":[{"field":"price","operator":"lt","value":"2000"}]}`, fiber.StatusOK)

	// Re-listing categories replaces manual links but rebuilds smart ones.
	res = pub("GET", "/api/v1/categories", "", fiber.StatusOK)
	var all struct {
		Categories []catResp `json:"categories"`
	}
	decodeJSON(t, readBody(t, res), &all)
	byID := map[string]catResp{}
	for _, c := range all.Categories {
		byID[c.ID] = c
	}
	u := byID[under.ID]
	if !u.IsSmart || len(u.Rules) != 1 || u.Rules[0].Value != "2000" {
		t.Fatalf("public /categories missing smart state: %+v", u)
	}
	if byID[manual.ID].IsSmart {
		t.Fatalf("manual category flagged smart: %+v", byID[manual.ID])
	}

	// --- Toggling smart off clears rules, keeps membership --------------------
	admin("PATCH", "/api/v1/categories/"+under.ID, `{"is_smart":false}`, fiber.StatusOK)
	res = pub("GET", "/api/v1/categories", "", fiber.StatusOK)
	all = struct {
		Categories []catResp `json:"categories"`
	}{}
	decodeJSON(t, readBody(t, res), &all)
	for _, c := range all.Categories {
		if c.ID == under.ID {
			if c.IsSmart || len(c.Rules) != 0 {
				t.Fatalf("smart-off category = %+v, want is_smart=false rules=[]", c)
			}
		}
	}

	// --- Delete cleans rules + membership rows --------------------------------
	admin("DELETE", "/api/v1/categories/"+under.ID, "", fiber.StatusOK)
	admin("DELETE", "/api/v1/categories/"+under.ID, "", fiber.StatusNotFound)
	admin("DELETE", "/api/v1/categories/"+manual.ID, "", fiber.StatusOK)
	if got = productsIn("under-20"); got[cheap] {
		t.Fatalf("product %s still under deleted category", cheap[:8])
	}

	// --- Tenant isolation: beta's own smart category + products ---------------
	adminB("POST", "/api/v1/categories",
		`{"name":"Beta Deals","slug":"beta-deals","is_smart":true,"rules":[{"field":"price","operator":"gt","value":"0"}]}`,
		fiber.StatusCreated)
	adminB("POST", "/api/v1/products", fmt.Sprintf(
		`{"name":"beta prod","slug":"beta-prod-%s","price_cents":99,"status":"active"}`, sfx), fiber.StatusCreated)
	dob := doTenant(bID, "")
	got = func() map[string]bool {
		res := dob("GET", "/api/v1/products?category=beta-deals", "", fiber.StatusOK)
		var out struct {
			Products []struct {
				ID string `json:"id"`
			} `json:"products"`
		}
		decodeJSON(t, readBody(t, res), &out)
		m := map[string]bool{}
		for _, p := range out.Products {
			m[p.ID] = true
		}
		return m
	}()
	if len(got) != 1 {
		t.Fatalf("beta smart category should hold exactly its own product, got %+v", got)
	}
	if len(productsIn("beta-deals")) != 0 {
		t.Fatalf("alpha storefront must never see beta's smart category")
	}

	// --- Bulk CSV import feeds smart collections ------------------------------
	rows := []bulkcsv.Row{
		{Line: 2, Name: "imported cheap", Slug: "imported-cheap-" + sfx, PriceCents: 1200, Currency: "usd", Status: "active"},
		{Line: 3, Name: "imported pricey", Slug: "imported-pricey-" + sfx, PriceCents: 9000, Currency: "usd", Status: "active"},
	}
	// Recreate a price<2000 smart category for alpha to absorb imported rows.
	admin("POST", "/api/v1/categories",
		`{"name":"Import Deals","is_smart":true,"rules":[{"field":"price","operator":"lt","value":"2000"}]}`,
		fiber.StatusCreated)
	rep, err := bulkcsv.ProcessImport(ctx, pool, aID, rows)
	if err != nil {
		t.Fatalf("bulk import: %v", err)
	}
	if rep.Imported != 2 {
		t.Fatalf("bulk import imported=%d want 2: %+v", rep.Imported, rep.Errors)
	}

	var importedCheap, importedPricey string
	rtx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin check tx: %v", err)
	}
	defer rtx.Rollback(ctx)
	if _, err := rtx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", aID); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := rtx.QueryRow(ctx,
		"SELECT id FROM products WHERE slug = $1", "imported-cheap-"+sfx).Scan(&importedCheap); err != nil {
		t.Fatalf("find imported product: %v", err)
	}
	if err := rtx.QueryRow(ctx,
		"SELECT id FROM products WHERE slug = $1", "imported-pricey-"+sfx).Scan(&importedPricey); err != nil {
		t.Fatalf("find imported product: %v", err)
	}

	got = productsIn("import-deals")
	// The <2000 smart set now holds every sub-$20 product: the two created
	// earlier and the imported row; the $90 row must stay out.
	if !got[cheap] || !got[mixed] || !got[importedCheap] || len(got) != 3 {
		t.Fatalf("import auto-membership wrong: cheap=%v mixed=%v importedCheap=%v all=%+v",
			got[cheap], got[mixed], got[importedCheap], got)
	}
	if got[importedPricey] {
		t.Fatalf("imported product priced 9000 wrongly joined the <2000 collection")
	}
}

func slugify(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

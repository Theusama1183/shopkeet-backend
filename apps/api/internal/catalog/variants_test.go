package catalog

import (
	"context"
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
	"github.com/shopkeet/api/internal/platform/httperr"
)

type optVal struct {
	ID        string `json:"id"`
	Value     string `json:"value"`
	SortOrder int    `json:"sort_order"`
}

type opt struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	SortOrder int      `json:"sort_order"`
	Values    []optVal `json:"values"`
}

type variant struct {
	ID             string `json:"id"`
	SKU            string `json:"sku"`
	PriceCents     int    `json:"price_cents"`
	InventoryCount int    `json:"inventory_count"`
	Status         string `json:"status"`
	OptionValues   []struct {
		OptionValueID string `json:"option_value_id"`
		OptionID      string `json:"option_id"`
		OptionName    string `json:"option_name"`
		Value         string `json:"value"`
	} `json:"option_values"`
}

type productDetail struct {
	ID             string    `json:"id"`
	PriceCents     int       `json:"price_cents"`
	InventoryCount int       `json:"inventory_count"`
	Options        []opt     `json:"options"`
	Variants       []variant `json:"variants"`
}

// TestVariantsRLSIsolation is the Phase 8 acceptance criterion
// (docs/07-expansion-build-spec.md): creating a product auto-creates a default
// variant; options + option values attach; concrete variants carry their own
// price/stock/SKU and link to option values; the cached products price/inventory
// (= MIN/SUM of active variants) recomputes on every variant mutation; option
// values from another product are rejected; SKUs are unique; the last variant
// cannot be deleted; the public storefront sees the full variant surface while
// tenant B (admin and storefront) cannot.
func TestVariantsRLSIsolation(t *testing.T) {
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

	const secret = "test-secret"
	sfx := randSuffix3()

	mkTenant := func(name string) tenantPair {
		var tid string
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "var-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tenantPair{id: tid, token: token}
	}
	a := mkTenant("alpha")
	b := mkTenant("beta")

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	RegisterRoutes(app.Group("/api/v1"), pool, secret, New(pool))

	do := func(method, path, token, tenant, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if tenant != "" {
			req.Header.Set("X-Tenant-ID", tenant)
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
	decodeJSON := func(res *http.Response, v any) {
		t.Helper()
		if err := json.NewDecoder(res.Body).Decode(v); err != nil {
			t.Fatalf("decode response: %v", err)
		}
	}

	// --- Create product: one default variant mirrors the flat price/stock ----
	pRes := do("POST", "/api/v1/products", a.token, "",
		`{"name":"Tee","slug":"tee-`+sfx+`","price_cents":1999,"inventory_count":10,"status":"active"}`,
		fiber.StatusCreated)
	var pa productDetail
	decodeJSON(pRes, &pa)
	if len(pa.Variants) != 1 {
		t.Fatalf("create must auto-create one default variant, got %d", len(pa.Variants))
	}
	if pa.Variants[0].PriceCents != 1999 || pa.Variants[0].InventoryCount != 10 || pa.Variants[0].SKU != "" {
		t.Fatalf("default variant must carry the flat price/stock/empty sku, got %+v", pa.Variants[0])
	}

	// --- Option + values ------------------------------------------------------
	oRes := do("POST", "/api/v1/products/"+pa.ID+"/options", a.token, "",
		`{"name":"Size","sort_order":1,"values":[{"value":"S","sort_order":1},{"value":"M","sort_order":2}]}`,
		fiber.StatusOK)
	var po productDetail
	decodeJSON(oRes, &po)
	if len(po.Options) != 1 || po.Options[0].Name != "Size" || len(po.Options[0].Values) != 2 {
		t.Fatalf("expected one Size option with 2 values, got %+v", po.Options)
	}
	valS := po.Options[0].Values[0].ID

	// --- Variant under that option value ---------------------------------------
	vRes := do("POST", "/api/v1/products/"+pa.ID+"/variants", a.token, "",
		`{"option_value_ids":["`+valS+`"],"sku":"TEE-S","price_cents":2499,"inventory_count":5,"status":"active"}`,
		fiber.StatusOK)
	var pv productDetail
	decodeJSON(vRes, &pv)
	if len(pv.Variants) != 2 {
		t.Fatalf("expected 2 variants now, got %d", len(pv.Variants))
	}
	// Cache = MIN active price / SUM active stock.
	if pv.PriceCents != 1999 || pv.InventoryCount != 15 {
		t.Fatalf("cached price/inventory should be MIN 1999 / SUM 15, got %d / %d", pv.PriceCents, pv.InventoryCount)
	}
	var newVar *variant
	for i := range pv.Variants {
		if pv.Variants[i].SKU == "TEE-S" {
			newVar = &pv.Variants[i]
		}
	}
	if newVar == nil {
		t.Fatalf("variant TEE-S not found, got %+v", pv.Variants)
	}
	if len(newVar.OptionValues) != 1 || newVar.OptionValues[0].Value != "S" || newVar.OptionValues[0].OptionName != "Size" {
		t.Fatalf("variant should link to Size/S, got %+v", newVar.OptionValues)
	}

	// --- Cross-product option value is rejected -------------------------------
	otherRes := do("POST", "/api/v1/products", a.token, "",
		`{"name":"Sock","slug":"sock-`+sfx+`","price_cents":500,"inventory_count":3,"status":"active"}`,
		fiber.StatusCreated)
	var other productDetail
	decodeJSON(otherRes, &other)
	oRes2 := do("POST", "/api/v1/products/"+other.ID+"/options", a.token, "",
		`{"name":"Color","values":[{"value":"Red"},{"value":"Blue"}]}`, fiber.StatusOK)
	var po2 productDetail
	decodeJSON(oRes2, &po2)
	do("POST", "/api/v1/products/"+pa.ID+"/variants", a.token, "",
		`{"option_value_ids":["`+po2.Options[0].Values[0].ID+`"],"price_cents":1}`,
		fiber.StatusBadRequest)

	// --- Duplicate SKU is rejected ---------------------------------------------
	do("POST", "/api/v1/products/"+pa.ID+"/variants", a.token, "",
		`{"sku":"TEE-S","price_cents":1}`, fiber.StatusConflict)

	// --- PATCH variant: cache recomputes ----------------------------------------
	puRes := do("PATCH", "/api/v1/products/"+pa.ID+"/variants/"+newVar.ID, a.token, "",
		`{"sku":"TEE-SM","price_cents":1599,"inventory_count":2}`, fiber.StatusOK)
	var pu productDetail
	decodeJSON(puRes, &pu)
	if pu.PriceCents != 1599 || pu.InventoryCount != 12 {
		t.Fatalf("after patch cache should be MIN 1599 / SUM 12, got %d / %d", pu.PriceCents, pu.InventoryCount)
	}

	// --- Public storefront sees the variant surface ------------------------------
	pubRes := do("GET", "/api/v1/products/"+pa.ID, "", a.id, "", fiber.StatusOK)
	var pub productDetail
	decodeJSON(pubRes, &pub)
	if len(pub.Variants) != 2 || len(pub.Options) != 1 {
		t.Fatalf("storefront should see 2 variants + 1 option, got %+v", pub)
	}

	// --- Tenant B is fully isolated ---------------------------------------------
	do("POST", "/api/v1/products/"+pa.ID+"/options", b.token, "", `{"name":"X"}`, fiber.StatusNotFound)
	do("POST", "/api/v1/products/"+pa.ID+"/variants", b.token, "", `{"price_cents":1}`, fiber.StatusNotFound)
	do("GET", "/api/v1/products/"+pa.ID, "", b.id, "", fiber.StatusNotFound)

	// --- Delete variant, then protect the last one --------------------------------
	do("POST", "/api/v1/products/"+pa.ID+"/variants", a.token, "",
		`{"sku":"TEE-X","price_cents":99,"inventory_count":1}`, fiber.StatusOK)
	delRes := do("DELETE", "/api/v1/products/"+pa.ID+"/variants/"+newVar.ID, a.token, "", "", fiber.StatusOK)
	var pd productDetail
	decodeJSON(delRes, &pd)
	if len(pd.Variants) != 2 || pd.PriceCents != 99 || pd.InventoryCount != 11 {
		t.Fatalf("after delete cache should be MIN 99 / SUM 11 over 2 variants, got %+v", pd)
	}
	// With two left, deleting one is fine…
	delRes2 := do("DELETE", "/api/v1/products/"+pa.ID+"/variants/"+pd.Variants[0].ID, a.token, "", "", fiber.StatusOK)
	var pf productDetail
	decodeJSON(delRes2, &pf)
	if len(pf.Variants) != 1 {
		t.Fatalf("expected 1 variant left, got %d", len(pf.Variants))
	}
	// …but the last remaining variant cannot be deleted.
	do("DELETE", "/api/v1/products/"+pa.ID+"/variants/"+pf.Variants[0].ID, a.token, "", "", fiber.StatusBadRequest)
}

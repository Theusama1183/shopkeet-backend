package catalog

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
)

func randSuffix3() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type tenantPair struct {
	id    string
	token string
}

type prod struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Slug       string `json:"slug"`
	Status     string `json:"status"`
	PriceCents int    `json:"price_cents"`
	Images     []struct {
		ID        string `json:"id"`
		SortOrder int    `json:"sort_order"`
	} `json:"images"`
	Categories []struct {
		Slug string `json:"slug"`
	} `json:"categories"`
}

// TestCatalogRLSIsolation is the Phase 3 acceptance criterion
// (docs/04-agent-build-spec.md): Merchant A creates a product with two images;
// Merchant B's storefront and admin API cannot see or edit it; only active
// products are publicly listed; images return in gallery order.
func TestCatalogRLSIsolation(t *testing.T) {
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
			name, "cat-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		// Seed a media asset owned by the tenant so product_images has an FK.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO media_assets (tenant_id, r2_key, url, content_type, size_bytes, alt_text)
			VALUES ($1, $2, $3, 'image/jpeg', 100, 'seed')`,
			tid, tid+"/seed.jpg", "https://pub.example/"+tid+"/seed.jpg"); err != nil {
			t.Fatalf("seed asset: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return tenantPair{id: tid, token: token}
	}
	a := mkTenant("alpha")
	b := mkTenant("beta")

	app := fiber.New()
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

	aAssetID := seedAssetID(t, pool, ctx, a.id)

	// --- Merchant A: create product (draft) + attach two images --------------
	slug := "widget-a-" + sfx
	pRes := do("POST", "/api/v1/products", a.token, "",
		`{"name":"Widget A","slug":"`+slug+`","description":"a great widget","price_cents":1999,"status":"draft"}`,
		fiber.StatusCreated)
	var pa prod
	decodeJSON(pRes, &pa)

	do("POST", "/api/v1/products/"+pa.ID+"/images", a.token, "",
		`{"media_asset_id":"`+aAssetID+`","sort_order":1}`, fiber.StatusOK)
	do("POST", "/api/v1/products/"+pa.ID+"/images", a.token, "",
		`{"media_asset_id":"`+aAssetID+`","sort_order":0}`, fiber.StatusConflict) // duplicate attach -> 409

	// Another asset (second object) so gallery order is real.
	aAsset2 := seedSecondAsset(t, pool, ctx, a.id)
	do("POST", "/api/v1/products/"+pa.ID+"/images", a.token, "",
		`{"media_asset_id":"`+aAsset2+`","sort_order":0}`, fiber.StatusOK)

	// --- Draft is invisible to the public storefront -------------------------
	pl := do("GET", "/api/v1/products", "", a.id, "", fiber.StatusOK)
	var list struct {
		Products []prod `json:"products"`
	}
	decodeJSON(pl, &list)
	if len(list.Products) != 0 {
		t.Fatalf("draft product must NOT be publicly listed, got %d", len(list.Products))
	}
	// Merchant A (admin) still sees its draft.
	do("GET", "/api/v1/products/"+pa.ID, a.token, "", "", fiber.StatusOK)

	// --- Activate; now public, images in gallery order -----------------------
	do("PATCH", "/api/v1/products/"+pa.ID, a.token, "",
		`{"status":"active"}`, fiber.StatusOK)

	pl = do("GET", "/api/v1/products", "", a.id, "", fiber.StatusOK)
	decodeJSON(pl, &list)
	if len(list.Products) != 1 || list.Products[0].Name != "Widget A" {
		t.Fatalf("active product should be listed publicly once, got %+v", list.Products)
	}
	if len(list.Products[0].Images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(list.Products[0].Images))
	}
	if list.Products[0].Images[0].SortOrder != 0 || list.Products[0].Images[1].SortOrder != 1 {
		t.Fatalf("images out of gallery order: %+v", list.Products[0].Images)
	}
	// search works on the active product
	sRes := do("GET", "/api/v1/products?search=widget", "", a.id, "", fiber.StatusOK)
	var sList struct {
		Products []prod `json:"products"`
	}
	decodeJSON(sRes, &sList)
	if len(sList.Products) != 1 {
		t.Fatalf("search should find the widget, got %d", len(sList.Products))
	}

	// --- Tenant B is fully isolated ------------------------------------------
	plB := do("GET", "/api/v1/products", "", b.id, "", fiber.StatusOK)
	var listB struct {
		Products []prod `json:"products"`
	}
	decodeJSON(plB, &listB)
	if len(listB.Products) != 0 {
		t.Fatalf("tenant B must not see tenant A's products, got %+v", listB.Products)
	}
	// B's admin can't edit or delete A's product.
	do("PATCH", "/api/v1/products/"+pa.ID, b.token, "",
		`{"name":"hack"}`, fiber.StatusNotFound)
	do("DELETE", "/api/v1/products/"+pa.ID, b.token, "", "", fiber.StatusNotFound)

	// B's storefront can't fetch A's active product either.
	do("GET", "/api/v1/products/"+pa.ID, "", b.id, "", fiber.StatusNotFound)

	// A can still delete its own product.
	do("DELETE", "/api/v1/products/"+pa.ID, a.token, "", "", fiber.StatusOK)
}

func seedAssetID(t *testing.T, pool *pgxpool.Pool, ctx context.Context, tid string) string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var id string
	if err := tx.QueryRow(ctx,
		"SELECT id FROM media_assets WHERE r2_key = $1", tid+"/seed.jpg").Scan(&id); err != nil {
		t.Fatalf("get seed asset: %v", err)
	}
	if id == "" {
		t.Fatal("empty seed asset id")
	}
	return id
}

func seedSecondAsset(t *testing.T, pool *pgxpool.Pool, ctx context.Context, tid string) string {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var id string
	if err := tx.QueryRow(ctx, `
		INSERT INTO media_assets (tenant_id, r2_key, url, content_type, size_bytes, alt_text)
		VALUES ($1, $2, $3, 'image/png', 200, 'second') RETURNING id`,
		tid, tid+"/second.png", "https://pub.example/"+tid+"/second.png").Scan(&id); err != nil {
		t.Fatalf("insert second asset: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit second asset: %v", err)
	}
	return id
}

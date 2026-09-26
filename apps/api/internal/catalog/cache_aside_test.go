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
	"github.com/shopkeet/api/internal/platform/cache"
	"github.com/shopkeet/api/internal/platform/httperr"
)

func randSuffixCache() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestProductCacheAside locks the Phase 14 cache-aside contract:
//   - a public storefront GET /products/:id is served from Redis once warmed,
//     keyed per tenant+product+viewer;
//   - any admin mutation (PATCH) invalidates that entry;
//   - the next public read repopulates it from Postgres.
//
// Requires REDIS_URL (the local Redis tunnel) alongside DATABASE_URL.
func TestProductCacheAside(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL not set; skipping cache-aside test")
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping cache-aside test")
	}

	ctx := context.Background()
	pool := mustPool(t, url)
	defer pool.Close()

	const secret = "test-secret"
	sfx := randSuffixCache()

	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"cache", "cache-"+sfx).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	rdb, err := cache.NewRedis(redisURL)
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	defer rdb.Close()

	svc := New(pool)
	svc.SetCache(rdb)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	RegisterRoutes(app.Group("/api/v1"), pool, secret, svc)

	doPublic := func(path string) *http.Response {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Tenant-ID", tid)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		return res
	}
	bodyOf := func(res *http.Response) string {
		t.Helper()
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		return string(raw)
	}

	slug := "cached-widget-" + sfx
	created := doProduct(t, app, pool, ctx, secret, tid, token, slug)
	var pid string
	if err := scanCreated(created.Body, &pid); err != nil {
		t.Fatalf("parse created: %v", err)
	}
	created.Body.Close()

	first := doPublic("/api/v1/products/" + pid)
	if first.StatusCode != fiber.StatusOK {
		t.Fatalf("first public read status %d", first.StatusCode)
	}
	firstBody := bodyOf(first)
	if !strings.Contains(firstBody, `"status":"active"`) {
		t.Fatalf("first read did not return active product: %s", firstBody)
	}

	// The cache now holds the entry: a second read must not hit Postgres, which
	// we prove by deleting the row and confirming the cached response repeats.
	if _, ok := rdb.Get(ctx, cache.ProductKey(tid, pid, "public")); !ok {
		t.Fatal("expected cached public entry after warm read")
	}
	deleteRow(t, pool, ctx, tid, pid)

	second := bodyOf(doPublic("/api/v1/products/" + pid))
	if second != firstBody {
		t.Fatalf("cached read differs from warm read:\ngot  %s\nwant %s", second, firstBody)
	}

	// An admin mutation invalidates the entry despite the row now being gone —
	// the merchant can't see it (deleted), and the next public read 404s.
	req := httptest.NewRequest("PATCH", "/api/v1/products/"+pid, strings.NewReader(`{"name":"Renamed"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	pres, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("PATCH: %v", err)
	}
	pres.Body.Close()
	if _, ok := rdb.Get(ctx, cache.ProductKey(tid, pid, "public")); ok {
		t.Fatal("cache not invalidated after admin PATCH")
	}
	if res := doPublic("/api/v1/products/" + pid); res.StatusCode != fiber.StatusNotFound {
		t.Fatalf("public read after invalidate+delete: status %d, want 404", res.StatusCode)
	}
}

func mustPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	return pool
}

func doProduct(t *testing.T, app *fiber.App, pool *pgxpool.Pool, ctx context.Context, secret, tid, token, slug string) *http.Response {
	t.Helper()
	req := httptest.NewRequest("POST", "/api/v1/products",
		strings.NewReader(`{"name":"Cached Widget","slug":"`+slug+`","price_cents":1999,"status":"active"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("create product: %v", err)
	}
	if res.StatusCode != fiber.StatusCreated {
		t.Fatalf("create product status %d", res.StatusCode)
	}
	return res
}

func scanCreated(r io.Reader, id *string) error {
	var body struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(r).Decode(&body); err != nil {
		return err
	}
	*id = body.ID
	return nil
}

func deleteRow(t *testing.T, pool *pgxpool.Pool, ctx context.Context, tid, productID string) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin delete tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM product_variants WHERE product_id = $1", productID); err != nil {
		t.Fatalf("delete variants: %v", err)
	}
	if _, err := tx.Exec(ctx, "DELETE FROM products WHERE id = $1", productID); err != nil {
		t.Fatalf("delete product: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit delete: %v", err)
	}
}
// External test package: exercises the Phase 30 per-tenant shopping feeds.
// Seeds a tenant with an active/in-stock product (with an image), an out-of-
// stock product, and a draft product, then validates the GMC XML and Meta CSV
// feeds: required fields present, correct price/availability rendering, and
// draft products excluded. Runs against a real database (skipped unless
// DATABASE_URL is set).
package feeds_test

import (
	"context"
	"crypto/rand"
	"encoding/csv"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/feeds"
	"github.com/shopkeet/api/internal/platform/httperr"
)

const appBase = "shopbase.test"

func TestProductFeeds(t *testing.T) {
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

	sfx := hex.EncodeToString(func() []byte {
		b := make([]byte, 4)
		_, _ = rand.Read(b)
		return b
	}())

	var tid string
	err = pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"feeds", "feed30-"+sfx).Scan(&tid)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// Tenant-scoped seeding: active+image product, out-of-stock product, and a
	// draft product (must never appear in the feed).
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	seedProduct := func(name, slug string, price, qty int, status string) string {
		t.Helper()
		var pid string
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, description, price_cents, currency, inventory_count, status)
			VALUES ($1, $2, $3, $4, $5, 'usd', $6, $7) RETURNING id`,
			tid, name, slug, "A "+name+" product.", price, qty, status).Scan(&pid); err != nil {
			t.Fatalf("seed product %s: %v", name, err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active')`, tid, pid, price, qty); err != nil {
			t.Fatalf("seed variant %s: %v", name, err)
		}
		return pid
	}
	var mediaID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO media_assets (tenant_id, r2_key, url, content_type, size_bytes, alt_text)
		VALUES ($1, $2, $3, 'image/jpeg', 100, 'hero') RETURNING id`,
		tid, tid+"/hero.jpg", "https://cdn.test/hero.jpg").Scan(&mediaID); err != nil {
		t.Fatalf("seed media: %v", err)
	}
	activeID := seedProduct("Denim Jacket", "denim-jacket", 4500, 7, "active")
	seedProduct("Empty Tee", "empty-tee", 1200, 0, "active")
	seedProduct("WIP Sweater", "wip-sweater", 3000, 3, "draft")
	if _, err := tx.Exec(ctx, `
		INSERT INTO product_images (tenant_id, product_id, media_asset_id, sort_order)
		VALUES ($1, $2, $3, 0)`, tid, activeID, mediaID); err != nil {
		t.Fatalf("seed product image: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	feeds.RegisterRoutes(v1, feeds.New(pool, appBase), pool)

	doGet := func(path string) string {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Tenant-ID", tid)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if res.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Fatalf("%s: status %d (body=%s)", path, res.StatusCode, string(b))
		}
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return string(b)
	}
	missingTenant := httptest.NewRequest("GET", "/api/v1/feeds/google-shopping.xml", nil)
	if res, err := app.Test(missingTenant, -1); err != nil || res.StatusCode != http.StatusBadRequest {
		t.Fatalf("feed without X-Tenant-ID: status %d err %v, want 400", res.StatusCode, err)
	}

	// --- Google XML feed ---------------------------------------------------------
	xmlBody := doGet("/api/v1/feeds/google-shopping.xml")
	base := "https://feed30-" + sfx + "." + appBase
	for _, want := range []string{"<g:id>", "<g:title>", "<g:condition>new", "<g:link>" + base + "/products/"} {
		if !strings.Contains(xmlBody, want) {
			t.Fatalf("xml feed missing %q:\n%s", want, xmlBody)
		}
	}
	if strings.Contains(xmlBody, "wip-sweater") {
		t.Fatalf("xml feed must not include draft products:\n%s", xmlBody)
	}
	if !strings.Contains(xmlBody, "<g:price>45.00 USD</g:price>") {
		t.Fatalf("xml feed price rendering wrong:\n%s", xmlBody)
	}
	if !strings.Contains(xmlBody, "<g:availability>in stock</g:availability>") ||
		!strings.Contains(xmlBody, "<g:availability>out of stock</g:availability>") {
		t.Fatalf("xml feed availability rendering wrong:\n%s", xmlBody)
	}
	if !strings.Contains(xmlBody, "<g:image_link>https://cdn.test/hero.jpg</g:image_link>") {
		t.Fatalf("xml feed missing image_link:\n%s", xmlBody)
	}

	// --- Meta CSV feed -----------------------------------------------------------
	csvBody := doGet("/api/v1/feeds/meta-catalog.csv")
	cr := csv.NewReader(strings.NewReader(csvBody))
	recs, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("meta csv unparseable: %v\n%s", err, csvBody)
	}
	if len(recs) != 3 { // header + 2 active products
		t.Fatalf("meta csv has %d rows, want 3:\n%s", len(recs), csvBody)
	}
	if got := recs[0]; got[0] != "id" || got[3] != "availability" || got[5] != "price" {
		t.Fatalf("meta csv header wrong: %+v", recs[0])
	}
	var denim []string
	for _, r := range recs[1:] {
		if strings.Contains(r[1], "Denim Jacket") {
			denim = r
		}
	}
	if denim == nil {
		t.Fatalf("meta csv missing Denim Jacket:\n%s", csvBody)
	}
	if denim[3] != "in stock" || denim[4] != "new" || denim[5] != "45.00_USD" {
		t.Fatalf("meta csv values wrong: %+v", denim)
	}
	if denim[6] != base+"/products/denim-jacket" {
		t.Fatalf("meta csv link wrong: %q", denim[6])
	}
	if denim[7] != "https://cdn.test/hero.jpg" {
		t.Fatalf("meta csv image wrong: %q", denim[7])
	}
}
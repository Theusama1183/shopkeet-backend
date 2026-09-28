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

// TestNotifyMe exercises POST /products/:id/variants/:variantId/notify-me
// (Phase 19 acceptance): only out-of-stock, non-preorderable variants accept a
// signup; a duplicate email (case-insensitive) is 409; in-stock or preorderable
// variants are refused; the subscription persists per tenant.
func TestNotifyMe(t *testing.T) {
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

	sfx := randSuffix3()
	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"notify-"+sfx, "notify-"+randSuffix3()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	seedVariant := func(slug string, inv int, allowPreorder bool, status string) (pid, vid string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'usd', $5, $6) RETURNING id`,
			tid, slug, "nm-"+slug+"-"+sfx, 1000, inv, status).Scan(&pid); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status, allow_preorder)
			VALUES ($1, $2, $3, $4, 'active', $5) RETURNING id`,
			tid, pid, 1000, inv, allowPreorder).Scan(&vid); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return pid, vid
	}
	oosPID, oosVID := seedVariant("oos", 0, false, "active")
	stockedPID, stockedVID := seedVariant("stocked", 5, false, "active")
	prePID, preVID := seedVariant("pre", 0, true, "active")
	draftPID, draftVID := seedVariant("draft", 0, false, "draft")

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	RegisterRoutes(app.Group("/api/v1"), pool, "x", New(pool))

	do := func(method, path, tenant, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
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
	notify := func(pid, vid, email string, want int) *http.Response {
		return do("POST", "/api/v1/products/"+pid+"/variants/"+vid+"/notify-me", tid,
			`{"email":"`+email+`"}`, want)
	}

	notify(oosPID, oosVID, "wait@example.com", fiber.StatusCreated)
	// Case-insensitive duplicate is a conflict.
	notify(oosPID, oosVID, "WAIT@example.com", fiber.StatusConflict)
	// In-stock and preorderable variants refuse signups.
	notify(stockedPID, stockedVID, "a@example.com", fiber.StatusBadRequest)
	notify(prePID, preVID, "b@example.com", fiber.StatusBadRequest)
	// Missing tenant header fails closed.
	do("POST", "/api/v1/products/"+oosPID+"/variants/"+oosVID+"/notify-me", "",
		`{"email":"c@example.com"}`, fiber.StatusBadRequest)
	// A draft (inactive) product's variant isn't a valid notify target.
	notify(draftPID, draftVID, "d@example.com", fiber.StatusNotFound)
	// Bad email.
	notify(oosPID, oosVID, "not-an-email", fiber.StatusBadRequest)
}

// TestVariantPreorderFields round-trips allow_preorder and preorder_ships_at
// through the admin create/patch surface and the product JSON.
func pstr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestVariantPreorderFields(t *testing.T) {
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

	sfx := randSuffix3()
	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"pre-fields-"+sfx, "pf-"+randSuffix3()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	var pid string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'usd', 0, 'active') RETURNING id`,
		tid, "pf", "pf-"+sfx, 1000).Scan(&pid); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	secret := "s"
	token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	RegisterRoutes(app.Group("/api/v1"), pool, secret, New(pool))

	do := func(method, path, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
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

	type variant struct {
		ID              string  `json:"id"`
		AllowPreorder   bool    `json:"allow_preorder"`
		PreorderShipsAt *string `json:"preorder_ships_at"`
	}

	do("POST", "/api/v1/products/"+pid+"/variants",
		`{"price_cents":1000,"inventory_count":0,"allow_preorder":true,"preorder_ships_at":"2026-11-01T09:00:00Z"}`,
		fiber.StatusOK)

	// Fetch is via the admin GET (PublicOrAdminMW); assert the JSON variant.
	res := do("GET", "/api/v1/products/"+pid, "", fiber.StatusOK)
	var product struct {
		Variants []variant `json:"variants"`
	}
	if err := json.NewDecoder(res.Body).Decode(&product); err != nil {
		t.Fatalf("decode product: %v", err)
	}
	if len(product.Variants) != 1 {
		t.Fatalf("want 1 variant, got %d", len(product.Variants))
	}
	vid := product.Variants[0].ID
	if !product.Variants[0].AllowPreorder {
		t.Fatal("allow_preorder should round-trip as true")
	}
	if product.Variants[0].PreorderShipsAt == nil || *product.Variants[0].PreorderShipsAt != "2026-11-01T09:00:00Z" {
		t.Fatalf("preorder_ships_at round-trip wrong: %q", pstr(product.Variants[0].PreorderShipsAt))
	}

	do("PATCH", "/api/v1/products/"+pid+"/variants/"+vid, `{"allow_preorder":false}`, fiber.StatusOK)
	res = do("GET", "/api/v1/products/"+pid, "", fiber.StatusOK)
	product = struct {
		Variants []variant `json:"variants"`
	}{}
	if err := json.NewDecoder(res.Body).Decode(&product); err != nil {
		t.Fatalf("decode product: %v", err)
	}
	if product.Variants[0].AllowPreorder {
		t.Fatal("allow_preorder should be patched to false")
	}
}
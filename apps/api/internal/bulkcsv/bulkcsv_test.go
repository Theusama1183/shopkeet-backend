// External test package: exercises the Phase 29 bulk CSV import/export.
// The import path is tested at the processor level (the enqueue/poll half needs
// Redis, which the acceptance suite doesn't assume): a 3-row CSV with one
// deliberately invalid row still imports the valid rows and reports the bad row
// by line number, exactly the spec's acceptance case. The synchronous export is
// exercised over HTTP. Runs against a real database (skipped unless
// DATABASE_URL is set).
package bulkcsv_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/csv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/bulkcsv"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// withTx runs fn inside a tenant-scoped RLS transaction so direct assertions
// are visible even when the suite runs as the app role (shopkeet_app).
func withTx(t *testing.T, ctx context.Context, pool *pgxpool.Pool, tid string, fn func(tx pgx.Tx)) {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	fn(tx)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func TestProductCSVImportExport(t *testing.T) {
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

	var tid, adminToken string
	err = pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"bulk", "bulk29-"+sfx).Scan(&tid)
	if err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	adminToken, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	// --- ParseCSV ---------------------------------------------------------------
	// Row 3 is deliberately malformed (bad status). ParseCSV is type-only so it
	// passes through; the processor reports it per line while row 2 imports.
	csvText := "name,slug,description,price_cents,currency,inventory_count,status\n" +
		"Alpha Shirt,,Comfortable tee,1500,usd,4,active\n" +
		"Beta Hat,,Warm beanie,900,usd,2,draft\n" +
		"Gamma Jeans,,,2500,usd,6,badstatus\n"
	rows, err := bulkcsv.ParseCSV(strings.NewReader(csvText))
	if err != nil {
		t.Fatalf("parse csv: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("parsed %d rows, want 3", len(rows))
	}

	// --- ProcessImport (worker path) --------------------------------------------
	rep, err := bulkcsv.ProcessImport(ctx, pool, tid, rows)
	if err != nil {
		t.Fatalf("process import: %v", err)
	}
	if rep.Total != 3 {
		t.Fatalf("report total %d, want 3", rep.Total)
	}
	if rep.Imported != 2 {
		t.Fatalf("report imported %d, want 2", rep.Imported)
	}
	if len(rep.Errors) != 1 || rep.Errors[0].Line != 4 || !strings.Contains(rep.Errors[0].Error, "badstatus") {
		t.Fatalf("report errors mismatch: %+v", rep.Errors)
	}

	// Both valid rows exist in the tenant, with auto-slugs from name; the
	// invalid row left nothing behind.
	withTx(t, ctx, pool, tid, func(tx pgx.Tx) {
		var n int
		if err := tx.QueryRow(ctx,
			"SELECT COUNT(*) FROM products WHERE tenant_id = $1", tid).Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		if n != 2 {
			t.Fatalf("tenant has %d products, want 2", n)
		}
		var aSlug, gSlug string
		if err := tx.QueryRow(ctx,
			"SELECT slug FROM products WHERE tenant_id = $1 AND name = 'Alpha Shirt'", tid).Scan(&aSlug); err != nil {
			t.Fatalf("alpha slug: %v", err)
		}
		if aSlug != "alpha-shirt" {
			t.Fatalf("alpha slug %q, want alpha-shirt", aSlug)
		}
		err := tx.QueryRow(ctx,
			"SELECT slug FROM products WHERE tenant_id = $1 AND name = 'Gamma Jeans'", tid).Scan(&gSlug)
		if err == nil {
			t.Fatalf("invalid row should not have inserted (slug=%s)", gSlug)
		}
	})

	// Duplicate generated slugs within one batch dedupe to slug, slug-2.
	dupeCsv := "name,price_cents\nDup Thing,100\nDup Thing,200\n"
	drows, err := bulkcsv.ParseCSV(strings.NewReader(dupeCsv))
	if err != nil {
		t.Fatalf("parse dupe csv: %v", err)
	}
	drep, err := bulkcsv.ProcessImport(ctx, pool, tid, drows)
	if err != nil {
		t.Fatalf("process dupe import: %v", err)
	}
	if drep.Imported != 2 {
		t.Fatalf("dupe import imported %d, want 2 (%+v)", drep.Imported, drep.Errors)
	}
	withTx(t, ctx, pool, tid, func(tx pgx.Tx) {
		rows, err := tx.Query(ctx,
			"SELECT slug FROM products WHERE tenant_id = $1 AND name = 'Dup Thing' ORDER BY id", tid)
		if err != nil {
			t.Fatalf("dup slugs query: %v", err)
		}
		slugs := []string{}
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				t.Fatalf("dup slug scan: %v", err)
			}
			slugs = append(slugs, s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			t.Fatalf("dup slugs err: %v", err)
		}
		if len(slugs) != 2 {
			t.Fatalf("duplicate batch produced %d slug rows, want 2: %v", len(slugs), slugs)
		}
		wantSet := map[string]bool{"dup-thing": true, "dup-thing-2": true}
		for _, s := range slugs {
			if !wantSet[s] {
				t.Fatalf("duplicate slugs not deduped: %v", slugs)
			}
		}
	})

	// --- HTTP export ------------------------------------------------------------
	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	bulkcsv.RegisterRoutes(v1, bulkcsv.New(pool, nil, nil), pool, secret)

	req := httptest.NewRequest("GET", "/api/v1/products/export", nil)
	req.Header.Set("Authorization", "Bearer "+adminToken)
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("export status %d, want 200", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if !strings.HasPrefix(string(body), "id,name,slug,description,price_cents,") {
		t.Fatalf("export missing header: %q", string(body))
	}
	if !strings.Contains(string(body), "alpha-shirt,Comfortable tee,1500,usd") {
		t.Fatalf("export missing imported product: %q", string(body))
	}
	// Export is a valid CSV with 4 rows (header + 4 products).
	cr := csv.NewReader(strings.NewReader(string(body)))
	recs, err := cr.ReadAll()
	if err != nil {
		t.Fatalf("export not valid csv: %v", err)
	}
	if len(recs) != 5 {
		t.Fatalf("export has %d rows, want 5 (header + 4 imported)", len(recs))
	}

	// Bad header CSV rejects at upload.
	if _, err := bulkcsv.ParseCSV(strings.NewReader("title,price\nT,1\n")); err == nil {
		t.Fatalf("missing name column should reject")
	}
}
// External test package (mirrors recommendations_test.go): the Phase 32
// acceptance — data-driven recommendations. A weekly recompute job turns order
// co-occurrence into type='auto' rows; manual curation outranks them.
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
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/recommendations"
)

// TestDataDrivenRecommendations is the Phase 32 criterion: once enough order
// history exists (a product pair co-occurs in >= 2 non-cancelled orders), the
// weekly recompute creates type='auto' recommendations that surface on GET
// /products/:id/recommendations; pairs seen once stay silent. A manual pick for
// the same pair always wins — both by hiding the stale auto row on read and by
// never regenerating it on the next recompute.
func TestDataDrivenRecommendations(t *testing.T) {
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
			name, "rec32-"+name+"-"+sfx).Scan(&tid)
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

	// order_items requires a variant_id, so every seeded product gets exactly
	// one variant and vidOf keeps the pairing for the item inserts below.
	vidOf := map[string]string{}
	seedProduct := func(tid string, name string) (prodID string) {
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
			VALUES ($1, $2, $3, 1000, 'usd', 10, 'active') RETURNING id`,
			tid, name, "rec32-"+name+"-"+sfx).Scan(&prodID); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		var vid string
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, 1000, 10, 'active') RETURNING id`,
			tid, prodID).Scan(&vid); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		vidOf[prodID] = vid
		return prodID
	}
	p1 := seedProduct(aid, "alpha-mug")
	p2 := seedProduct(aid, "alpha-tee")
	p3 := seedProduct(aid, "alpha-cap")
	p4 := seedProduct(aid, "alpha-once")
	p5 := seedProduct(aid, "alpha-rare")
	betaProd := seedProduct(bid, "beta-tote")

	// Order history inside alpha. (p1,p2) co-occurs 3x, (p1,p3) 2x -> both
	// recommended for p1; p4 and p5 co-occur with p1 only once -> stay silent.
	seedOrder := func(tid string, productIDs ...string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin order: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var oid string
		if err := tx.QueryRow(ctx, `
			INSERT INTO orders (tenant_id, customer_name, customer_phone, shipping_address, status, total_cents)
			VALUES ($1, 'Pride', '+1-555-' || $2, '1 Main St', 'confirmed', 1000) RETURNING id`,
			tid, sfx).Scan(&oid); err != nil {
			t.Fatalf("seed order: %v", err)
		}
		for _, pid := range productIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO order_items (tenant_id, order_id, product_id, variant_id, quantity, unit_price_cents)
				VALUES ($1, $2, $3, $4, 1, 1000)`, tid, oid, pid, vidOf[pid]); err != nil {
				t.Fatalf("seed item: %v", err)
			}
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit order: %v", err)
		}
	}
	seedOrder(aid, p1, p2, p3)
	seedOrder(aid, p1, p2, p4)
	seedOrder(aid, p1, p2)
	seedOrder(aid, p1, p3)
	seedOrder(aid, p2, p3)
	seedOrder(bid, betaProd, betaProd) // beta has zero meaningful co-occurrence

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	recommendations.RegisterRoutes(v1, pool, secret, recommendations.New(pool))

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
	admin := client(aid, aToken)
	betaAdmin := client(bid, bToken)
	pub := client(aid, "")

	fetch := func(c func(method, path, body string, want int) *http.Response, productID string) []recRow {
		res := c("GET", "/api/v1/products/"+productID+"/recommendations", "", fiber.StatusOK)
		var body struct {
			Recommendations []recRow `json:"recommendations"`
		}
		if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
			t.Fatalf("decode recs: %v", err)
		}
		return body.Recommendations
	}

	// --- Run the weekly job exactly like the scheduled task does. ---
	if _, err := recommendations.RecomputeAllTenants(ctx, pool); err != nil {
		t.Fatalf("recompute: %v", err)
	}

	// Auto picks surface on the public storefront, ordered by co-occurrence.
	picks := fetch(pub, p1)
	if len(picks) != 2 {
		t.Fatalf("p1 should get 2 auto picks, got %d: %+v", len(picks), picks)
	}
	if picks[0].Recommended.ID != p2 || picks[1].Recommended.ID != p3 {
		t.Fatalf("p1 auto picks should be p2 (3x) then p3 (2x), got %+v", picks)
	}
	for _, r := range picks {
		if r.Type != "auto" {
			t.Fatalf("computed pick should carry type=auto, got %q", r.Type)
		}
	}
	// Single-co-occurrence pairs never surface.
	oncePicks := fetch(pub, p2)
	for _, r := range oncePicks {
		if r.Recommended.ID == p4 || r.Recommended.ID == p5 {
			t.Fatalf("p4/p5 co-occur once and must stay silent, got %+v", oncePicks)
		}
	}
	// Tenant isolation: beta's history produces nothing, and beta can't see
	// alpha's picks.
	betaPub := client(bid, "")
	if len(fetch(betaPub, betaProd)) != 0 {
		t.Fatalf("beta should have no auto picks")
	}
	betaAdmin("GET", "/api/v1/products/"+p1+"/recommendations", "", fiber.StatusNotFound)

	// --- Manual curation outranks a computed pick. ---
	res := admin("POST", "/api/v1/products/"+p1+"/recommendations",
		`{"recommended_product_id":"`+p2+`"}`, fiber.StatusCreated)
	if res.StatusCode != fiber.StatusCreated {
		t.Fatalf("manual curation should succeed, got %d", res.StatusCode)
	}
	// Read-side: the manual pick hides the stale auto row for the same pair and
	// sorts first.
	picks = fetch(pub, p1)
	if len(picks) != 2 {
		t.Fatalf("manual override should still yield 2 visible picks, got %d: %+v", len(picks), picks)
	}
	if picks[0].Type != "manual" || picks[0].Recommended.ID != p2 ||
		picks[1].Type != "auto" || picks[1].Recommended.ID != p3 {
		t.Fatalf("manual p2 should lead, auto p3 follow, got %+v", picks)
	}
	// Recompute-side: the job must not resurrect the overridden pair.
	if _, err := recommendations.RecomputeAllTenants(ctx, pool); err != nil {
		t.Fatalf("recompute after manual override: %v", err)
	}
	var autoCount int
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin check tx: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", aid); err != nil {
		t.Fatalf("set check tenant: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT count(*) FROM product_recommendations
		 WHERE product_id = $1 AND recommended_product_id = $2 AND type = 'auto'`,
		p1, p2).Scan(&autoCount); err != nil {
		t.Fatalf("auto count: %v", err)
	}
	if autoCount != 0 {
		t.Fatalf("manual override must suppress the auto row on recompute, got %d auto rows for (p1,p2)", autoCount)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit check tx: %v", err)
	}
	picks = fetch(pub, p1)
	if len(picks) != 2 || picks[0].Type != "manual" || picks[1].Type != "auto" {
		t.Fatalf("after recompute the visible set must be unchanged, got %+v", picks)
	}
}
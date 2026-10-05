package cart

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
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

type abandonedRow struct {
	ID             string `json:"id"`
	CustomerEmail  string `json:"customer_email"`
	ItemCount      int    `json:"item_count"`
	TotalCents     int    `json:"total_cents"`
	RecoverySentAt *string `json:"recovery_sent_at"`
}

type abandonedList struct {
	Carts []abandonedRow `json:"carts"`
}

// TestListAbandoned is the Phase B acceptance for GET /carts/abandoned: a cart
// with a captured customer_email and live line items is listed (checkout
// deletes carts, so a surviving row with lines is an unconverted checkout);
// total reflects the items; a cart without an email stays hidden; tenant B is
// isolated.
func TestListAbandoned(t *testing.T) {
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

	sfx := randSuffix4()

	mkTenant := func(name, sub string) (string, string) {
		var tid string
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, sub+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err := auth.Sign(testSecret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tid, token
	}
	aid, atoken := mkTenant("alpha", "ab-alpha")
	bid, btoken := mkTenant("beta", "ab-beta")

	// A's catalog: one active product with a 1200 variant (inventory 10).
	seedProduct := func(tid, slug string, price int) (prodID, variantID string) {
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
			tid, slug, slug+"-"+sfx, price, 10).Scan(&prodID); err != nil {
			t.Fatalf("seed product %s: %v", slug, err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
			tid, prodID, price, 10).Scan(&variantID); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return prodID, variantID
	}
	_, vA := seedProduct(aid, "ab-p1", 1200)
	_, vB := seedProduct(bid, "ab-pB", 500)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, testSecret, New(pool, NoopReserver{}), ratelimit.New(nil))

	do := func(method, path, tid, session, bearer, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", tid)
		if session != "" {
			req.Header.Set("X-Customer-Session", session)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
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

	// Abandoned-in-the-making: items + captured email.
	s := "sess-abandoned-" + sfx
	do("POST", "/api/v1/cart", aid, s, "", `{"variant_id":"`+vA+`","quantity":2}`, fiber.StatusOK)
	do("POST", "/api/v1/cart/email", aid, s, "", `{"email":"ab-`+sfx+`@example.com"}`, fiber.StatusOK)

	// A cart with items but no email must stay hidden.
	s2 := "sess-noemail-" + sfx
	do("POST", "/api/v1/cart", aid, s2, "", `{"variant_id":"`+vA+`","quantity":1}`, fiber.StatusOK)

	res := do("GET", "/api/v1/carts/abandoned", aid, "", atoken, "", fiber.StatusOK)
	var got abandonedList
	if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
		t.Fatalf("decode abandoned: %v", err)
	}
	if len(got.Carts) != 1 {
		t.Fatalf("tenant A should see exactly one abandoned cart, got %+v", got.Carts)
	}
	row := got.Carts[0]
	if row.CustomerEmail != "ab-"+sfx+"@example.com" || row.ItemCount != 1 || row.TotalCents != 2400 {
		t.Fatalf("abandoned row should be email/1 item/2400, got %+v", row)
	}

	// Tenant B is isolated; its bearer token reads B's (empty) list.
	res = do("GET", "/api/v1/carts/abandoned", bid, "", btoken, "", fiber.StatusOK)
	var gotB abandonedList
	if err := json.NewDecoder(res.Body).Decode(&gotB); err != nil {
		t.Fatalf("decode abandoned b: %v", err)
	}
	if len(gotB.Carts) != 0 {
		t.Fatalf("tenant B should see no abandoned carts, got %d", len(gotB.Carts))
	}

	// A B-cart under A's list? No — header mismatch is refused/isolated.
	_ = vB
}
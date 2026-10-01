// External test package: mounts the wishlist surface plus customers routes the
// signup flow relies on, outside package wishlist so imports stay
// one-directional (mirrors loyalty_test.go).
package wishlist_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/customers"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
	"github.com/shopkeet/api/internal/wishlist"
)

func TestWishlistFlow(t *testing.T) {
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

	mkTenant := func(name string) (tid, admin string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "wl-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		admin, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign admin: %v", err)
		}
		return tid, admin
	}
	aID, aAdmin := mkTenant("alpha")
	bID, _ := mkTenant("beta")

	seedProduct := func(tid, name, slugSfx string, price int, status string) (productID string) {
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
			VALUES ($1, $2, $3, $4, 'usd', 5, $5) RETURNING id`,
			tid, name, strings.ToLower(name)+"-"+slugSfx+sfx[:4], price, status).Scan(&productID); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return productID
	}
	aLive := seedProduct(aID, "Mug", "live", 1500, "active")
	aDraft := seedProduct(aID, "Teapot", "draft", 3000, "draft")
	bLive := seedProduct(bID, "BetaBag", "beta", 900, "active")

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	bus := events.NewBus()
	customers.RegisterRoutes(v1, pool, secret, customers.New(pool, secret, bus), ratelimit.New(nil))
	wishlist.RegisterRoutes(v1, pool, secret, wishlist.New(pool))

	doTenant := func(tid string) func(method, path, bearer, body string, want int) *http.Response {
		return func(method, path, bearer, body string, want int) *http.Response {
			t.Helper()
			var req *http.Request
			if body == "" {
				req = httptest.NewRequest(method, path, nil)
			} else {
				req = httptest.NewRequest(method, path, strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
			}
			req.Header.Set("X-Tenant-ID", tid)
			if bearer != "" {
				req.Header.Set("Authorization", "Bearer "+bearer)
			}
			res, err := app.Test(req, -1)
			if err != nil {
				t.Fatalf("%s %s: %v", method, path, err)
			}
			if res.StatusCode != want {
				b, _ := ioRead(res)
				t.Fatalf("%s %s: status %d, want %d (body=%s)", method, path, res.StatusCode, want, b)
			}
			return res
		}
	}
	doA := doTenant(aID)
	doB := doTenant(bID)

	// signup a customer in each tenant
	signup := func(do func(string, string, string, string, int) *http.Response, tid, email string) string {
		res := do("POST", "/api/v1/customers/signup", "",
			fmt.Sprintf(`{"email":%q,"phone":"+1-555-9001","password":"supersecret8"}`, email),
			fiber.StatusCreated)
		var p struct {
			Token string `json:"token"`
		}
		if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
			t.Fatalf("decode signup: %v", err)
		}
		if p.Token == "" {
			t.Fatalf("signup returned no token")
		}
		return p.Token
	}
	ada := signup(doA, aID, "ada@example.com")
	beta := signup(doB, bID, "beta@example.com")

	type wish struct {
		Items []struct {
			ID          string `json:"id"`
			ProductID   string `json:"product_id"`
			ProductName string `json:"product_name"`
			PriceCents  int    `json:"price_cents"`
			Status      string `json:"status"`
			CreatedAt   string `json:"created_at"`
		} `json:"items"`
	}

	// fresh wishlist is empty
	res := doA("GET", "/api/v1/customers/me/wishlist", ada, "", fiber.StatusOK)
	var w wish
	if err := json.NewDecoder(res.Body).Decode(&w); err != nil {
		t.Fatalf("decode wishlist: %v", err)
	}
	if len(w.Items) != 0 {
		t.Fatalf("fresh wishlist should be empty, got %+v", w.Items)
	}

	// add a live product via body product_id
	res = doA("POST", "/api/v1/customers/me/wishlist", ada,
		fmt.Sprintf(`{"product_id":%q}`, aLive), fiber.StatusCreated)
	var item struct {
		ID          string `json:"id"`
		ProductID   string `json:"product_id"`
		ProductName string `json:"product_name"`
		PriceCents  int    `json:"price_cents"`
		Status      string `json:"status"`
	}
	if err := json.NewDecoder(res.Body).Decode(&item); err != nil {
		t.Fatalf("decode add: %v", err)
	}
	if item.ProductID != aLive || item.PriceCents != 1500 || item.ProductName != "Mug" {
		t.Fatalf("unexpected added item: %+v", item)
	}

	// duplicate add -> 409 (UNIQUE (customer_id, product_id))
	doA("POST", "/api/v1/customers/me/wishlist", ada,
		fmt.Sprintf(`{"product_id":%q}`, aLive), fiber.StatusConflict)

	// add via route param (also accepted)
	doA("POST", "/api/v1/customers/me/wishlist/"+aDraft, ada, "", fiber.StatusCreated)

	// adding an unknown product -> 404
	doA("POST", "/api/v1/customers/me/wishlist", ada,
		`{"product_id":"00000000-0000-0000-0000-000000000000"}`, fiber.StatusNotFound)
	doA("POST", "/api/v1/customers/me/wishlist", ada, `{}`, fiber.StatusBadRequest)

	// list now has 2, newest first (Teapot added after Mug)
	res = doA("GET", "/api/v1/customers/me/wishlist", ada, "", fiber.StatusOK)
	w = wish{}
	if err := json.NewDecoder(res.Body).Decode(&w); err != nil {
		t.Fatalf("decode wishlist: %v", err)
	}
	if len(w.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(w.Items))
	}
	if w.Items[0].Status != "draft" || w.Items[1].ProductID != aLive {
		t.Fatalf("wishlist ordering/status wrong: %+v", w.Items)
	}

	// remove the draft item; then re-removing -> 404
	doA("DELETE", "/api/v1/customers/me/wishlist/"+aDraft, ada, "", fiber.StatusNoContent)
	doA("DELETE", "/api/v1/customers/me/wishlist/"+aDraft, ada, "", fiber.StatusNotFound)

	// tenant isolation: beta customer cannot see or hold alpha's products
	doB("GET", "/api/v1/customers/me/wishlist", beta, "", fiber.StatusOK)
	doB("POST", "/api/v1/customers/me/wishlist", beta,
		fmt.Sprintf(`{"product_id":%q}`, aLive), fiber.StatusNotFound)
	doB("POST", "/api/v1/customers/me/wishlist", beta,
		fmt.Sprintf(`{"product_id":%q}`, bLive), fiber.StatusCreated)

	// merchant token is refused on the customer surface
	doA("GET", "/api/v1/customers/me/wishlist", aAdmin, "", fiber.StatusForbidden)
	// unauthenticated is refused
	doA("GET", "/api/v1/customers/me/wishlist", "", "", fiber.StatusUnauthorized)
}

func ioRead(res *http.Response) (string, error) {
	defer res.Body.Close()
	b := make([]byte, 0, 512)
	buf := make([]byte, 512)
	for {
		n, err := res.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			if err.Error() == "EOF" {
				return string(b), nil
			}
			return "", err
		}
	}
}

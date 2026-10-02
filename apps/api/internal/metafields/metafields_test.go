// External test package: exercises the Phase 28 custom-fields (metafields)
// surface end to end — merchant PUTs a metafield, the public GET /products/:id
// embeds it, list/delete work, and a sibling tenant never sees it. Runs against
// a real database (skipped unless DATABASE_URL is set), mirroring the
// affiliates/loyalty acceptance tests.
package metafields_test

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
	"github.com/shopkeet/api/internal/catalog"
	"github.com/shopkeet/api/internal/metafields"
	"github.com/shopkeet/api/internal/platform/httperr"
)

type productDetail struct {
	Metafields []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
		Type  string `json:"type"`
	} `json:"metafields"`
}

func readBody(t *testing.T, res *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(res.Body)
	res.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return string(b)
}

func TestProductMetafields(t *testing.T) {
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

	mkTenant := func(name string) (tid, adminToken string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "mf28-"+name+"-"+sfx).Scan(&tid)
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
	bID, _ := mkTenant("beta")

	// seedProduct adds one active product with its default variant.
	seedProduct := func(tid string) (productID string) {
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
			tid, "mf28-prod", "mf28-prod-"+sfx+tid[:4], 1000, 5).Scan(&productID); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active')`, tid, productID, 1000, 5); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return productID
	}
	aProd := seedProduct(aID)
	seedProduct(bID)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	mfSvc := metafields.New(pool)
	metafields.RegisterRoutes(v1, mfSvc, pool, secret)
	catalog.RegisterRoutes(v1, pool, secret, catalog.New(pool))

	doTenant := func(tid string) func(method, path, bearer, body string, want int) *http.Response {
		return func(method, path, bearer, body string, want int) *http.Response {
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
				t.Fatalf("%s %s: status %d, want %d (body=%s)", method, path,
					res.StatusCode, want, readBody(t, res))
			}
			return res
		}
	}
	doA := doTenant(aID)
	doB := doTenant(bID)

	// Write then update a metafield.
	doA("PUT", "/api/v1/products/"+aProd+"/metafields/material", aAdmin,
		`{"value":"cotton","type":"text"}`, fiber.StatusOK)
	doA("PUT", "/api/v1/products/"+aProd+"/metafields/material", aAdmin,
		`{"value":"linen","type":"text"}`, fiber.StatusOK)

	// Public product detail embeds it.
	res := doA("GET", "/api/v1/products/"+aProd, "", "", fiber.StatusOK)
	var det productDetail
	if err := json.NewDecoder(strings.NewReader(readBody(t, res))).Decode(&det); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	var got string
	for _, m := range det.Metafields {
		if m.Key == "material" {
			got = m.Value
		}
	}
	if got != "linen" {
		t.Fatalf("public product metafields.material = %q, want %q", got, "linen")
	}

	// Second metafield; admin list exposes both sorted by key.
	doA("PUT", "/api/v1/products/"+aProd+"/metafields/weight", aAdmin,
		`{"value":"250","type":"number"}`, fiber.StatusOK)
	res = doA("GET", "/api/v1/products/"+aProd+"/metafields", aAdmin, "", fiber.StatusOK)
	var list struct {
		Metafields []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		} `json:"metafields"`
	}
	if err := json.NewDecoder(strings.NewReader(readBody(t, res))).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Metafields) != 2 || list.Metafields[0].Key != "material" || list.Metafields[1].Key != "weight" {
		t.Fatalf("admin metafield list mismatch: %+v", list.Metafields)
	}

	// Tenant B never sees tenant A's metafields on its own product.
	resB := doB("GET", "/api/v1/products/"+aProd, "", "", fiber.StatusNotFound)
	_ = resB

	// Bad type rejected.
	doA("PUT", "/api/v1/products/"+aProd+"/metafields/oops", aAdmin,
		`{"value":"x","type":"enum"}`, fiber.StatusBadRequest)

	// Delete removes it from the public view.
	doA("DELETE", "/api/v1/products/"+aProd+"/metafields/material", aAdmin, "", fiber.StatusOK)
	res = doA("GET", "/api/v1/products/"+aProd, "", "", fiber.StatusOK)
	det = productDetail{}
	if err := json.NewDecoder(strings.NewReader(readBody(t, res))).Decode(&det); err != nil {
		t.Fatalf("decode detail after delete: %v", err)
	}
	for _, m := range det.Metafields {
		if m.Key == "material" {
			t.Fatalf("material metafield still present after delete: %+v", det.Metafields)
		}
	}
	doA("DELETE", "/api/v1/products/"+aProd+"/metafields/material", aAdmin, "", fiber.StatusNotFound)
	doA("GET", "/api/v1/products/does-not-exist/metafields", aAdmin, "", fiber.StatusNotFound)
}
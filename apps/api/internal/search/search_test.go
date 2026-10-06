package search

import (
	"context"
	"encoding/json"
	"io"
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

// TestSearchReconciliation is the integration half of the command-palette
// search (Phase 35 §3): a seeded tenant's products/orders/customers each match
// on their search_vector column through the same plainto_tsquery predicate the
// catalog list uses, and a second tenant's admin sees nothing (no cross-tenant
// bleed). Gated on DATABASE_URL like every integration test in this API.
func TestSearchReconciliation(t *testing.T) {
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
	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"search", "sr-"+sfx()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	adminToken, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	seedTx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer seedTx.Rollback(ctx)
	if _, err := seedTx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	hub := "Kiteboard Hub"
	var kitID string
	if err := seedTx.QueryRow(ctx, `
		INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		VALUES ($1, $2, $3, 4500, 'usd', 12, 'active') RETURNING id`,
		tid, hub, "kiteboard-hub-"+sfx()).Scan(&kitID); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if _, err := seedTx.Exec(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		VALUES ($1, $2, 4500, 12, 'active')`, tid, kitID); err != nil {
		t.Fatalf("seed variant: %v", err)
	}

	var orderID string
	if err := seedTx.QueryRow(ctx, `
		INSERT INTO orders (tenant_id, customer_name, customer_phone, status,
		                    total_cents, currency, shipping_cost_cents)
		VALUES ($1, $2, $3, 'confirmed', $4, 'usd', 0) RETURNING id`,
		tid, "Noah Kite", "+1-555-0101", 4500).Scan(&orderID); err != nil {
		t.Fatalf("seed order: %v", err)
	}

	var customerID string
	if err := seedTx.QueryRow(ctx, `
		INSERT INTO customers (tenant_id, email, phone)
		VALUES ($1, $2, $3) RETURNING id`,
		tid, "noah@kitebooking.example", "+1-555-0101").Scan(&customerID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	// One row that should NOT match "kite" in any group.
	if _, err := seedTx.Exec(ctx, `
		INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		VALUES ($1, 'Linen Duvet', $2, 7000, 'usd', 3, 'draft')`,
		tid, "linen-duvet-"+sfx()); err != nil {
		t.Fatalf("seed decoy product: %v", err)
	}
	if err := seedTx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, secret, New(pool))

	do := func(path string, want int) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1"+path, nil)
		req.Header.Set("Authorization", "Bearer "+adminToken)
		req.Header.Set("X-Tenant-ID", tid)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != want {
			t.Fatalf("GET %s: status %d, want %d (body=%s)", path, res.StatusCode, want, raw)
		}
		return string(raw)
	}

	type searchResp struct {
		Products []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"products"`
		Orders []struct {
			ID           string `json:"id"`
			CustomerName string `json:"customer_name"`
			Status       string `json:"status"`
			TotalCents   int64  `json:"total_cents"`
			Currency     string `json:"currency"`
		} `json:"orders"`
		Customers []struct {
			ID    string `json:"id"`
			Email string `json:"email"`
			Phone string `json:"phone"`
		} `json:"customers"`
	}

	var sr searchResp
	if err := json.Unmarshal([]byte(do("/search?q=kite", fiber.StatusOK)), &sr); err != nil {
		t.Fatalf("decode search: %v", err)
	}
	if len(sr.Products) != 1 || sr.Products[0].Name != "Kiteboard Hub" || sr.Products[0].Status != "active" {
		t.Fatalf("product group wrong: %+v", sr.Products)
	}
	if len(sr.Orders) != 1 || sr.Orders[0].CustomerName != "Noah Kite" ||
		sr.Orders[0].Status != "confirmed" || sr.Orders[0].TotalCents != 4500 || sr.Orders[0].Currency != "usd" {
		t.Fatalf("order group wrong: %+v", sr.Orders)
	}
	if len(sr.Customers) != 1 || sr.Customers[0].Email != "noah@kitebooking.example" ||
		sr.Customers[0].Phone != "+1-555-0101" {
		t.Fatalf("customer group wrong: %+v", sr.Customers)
	}

	// Phone matches a customer even without the name.
	if err := json.Unmarshal([]byte(do("/search?q=555-0101", fiber.StatusOK)), &sr); err != nil {
		t.Fatalf("decode phone search: %v", err)
	}
	if len(sr.Orders) != 1 || len(sr.Customers) != 1 {
		t.Fatalf("phone search should hit order + customer: %+v", sr)
	}

	// Decoy never surfaces; a nonsense query returns empty (not error) groups.
	if err := json.Unmarshal([]byte(do("/search?q=linen", fiber.StatusOK)), &sr); err != nil {
		t.Fatalf("decode decoy search: %v", err)
	}
	if len(sr.Products) != 1 || sr.Products[0].Name != "Linen Duvet" {
		t.Fatalf("decoy should match on its own query: %+v", sr.Products)
	}
	if err := json.Unmarshal([]byte(do("/search?q=zzz-nope", fiber.StatusOK)), &sr); err != nil {
		t.Fatalf("decode empty search: %v", err)
	}
	if len(sr.Products) != 0 || len(sr.Orders) != 0 || len(sr.Customers) != 0 {
		t.Fatalf("nonsense query should return empty groups: %+v", sr)
	}

	// Validation: missing + over-long q are 400s.
	do("/search", fiber.StatusBadRequest)
	do("/search?q="+strings.Repeat("a", 101), fiber.StatusBadRequest)

	// RLS: tenant B's admin sees empty groups (no cross-tenant bleed).
	var tidB string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"search-b", "sr-b-"+sfx()).Scan(&tidB); err != nil {
		t.Fatalf("seed tenant B: %v", err)
	}
	tokB, _ := auth.Sign(secret, tidB, tidB[:8], "owner", time.Hour)
	req := httptest.NewRequest("GET", "/api/v1/search?q=kite", nil)
	req.Header.Set("Authorization", "Bearer "+tokB)
	req.Header.Set("X-Tenant-ID", tidB)
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("tenant B search: %v", err)
	}
	rawB, _ := io.ReadAll(res.Body)
	res.Body.Close()
	var tb searchResp
	if err := json.Unmarshal(rawB, &tb); err != nil {
		t.Fatalf("decode tenant B search: %v", err)
	}
	if len(tb.Products) != 0 || len(tb.Orders) != 0 || len(tb.Customers) != 0 {
		t.Fatalf("tenant B saw tenant A's data: %+v", tb)
	}
}

func sfx() string {
	return strings.ReplaceAll(time.Now().Format("150405.000000"), ".", "")
}
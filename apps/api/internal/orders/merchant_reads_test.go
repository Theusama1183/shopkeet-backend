package orders

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
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// TestAdminOrderReads is the Phase B acceptance: a merchant can read any single
// order in their tenant from GET /orders/:id (no phone needed — the customer
// path still demands it), sees internal_note on the admin read, and the admin
// list supports the status/payment_status/source filters the Orders and Drafts
// pages rely on. Tenant B stays isolated.
func TestAdminOrderReads(t *testing.T) {
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
	sfx := randSuffix5()

	mkTenant := func(name string) tenantO {
		var tid string
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "rd-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tenantO{id: tid, token: token}
	}
	a := mkTenant("alpha")
	b := mkTenant("beta")

	_, v1a, v1b := seedReturnProduct(t, ctx, pool, a.id, "p1", sfx)
	stdRate := seedReturnRate(t, ctx, pool, a.id)

	bus := events.NewBus()
	svc := New(pool, bus, payments.NewRegistry())

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, secret, svc, ratelimit.New(nil))

	do := func(method, path, bearer, body string, want int) *http.Response {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("X-Tenant-ID", a.id)
		req.Header.Set("Authorization", "Bearer "+bearer)
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

	// Create the order via the draft flow — same write path as checkout, and it
	// returns the full order object to work against.
	phone := "+92-31x-" + sfx
	res := do("POST", "/api/v1/orders/draft", a.token, `{
		"customer_name":"Ada","customer_phone":"`+phone+`","customer_email":"ada-rd-`+sfx+`@example.com",
		"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",
		"shipping_rate_id":"`+stdRate+`",
		"lines":[{"variant_id":"`+v1a+`","quantity":2},{"variant_id":"`+v1b+`","quantity":1}]}`,
		fiber.StatusCreated)
	var draft draftPayload
	if err := json.NewDecoder(res.Body).Decode(&draft); err != nil {
		t.Fatalf("decode draft: %v", err)
	}
	orderID := draft.ID

	// Set an internal note, then read it back on the admin GET.
	do("PATCH", "/api/v1/orders/"+orderID+"/note", a.token, `{"note":"call before dispatch"}`, fiber.StatusOK)
	res = do("GET", "/api/v1/orders/"+orderID, a.token, "", fiber.StatusOK)
	var single struct {
		ID           string `json:"id"`
		InternalNote string `json:"internal_note"`
		CustomerName string `json:"customer_name"`
	}
	if err := json.NewDecoder(res.Body).Decode(&single); err != nil {
		t.Fatalf("decode admin order: %v", err)
	}
	if single.ID != orderID || single.CustomerName != "Ada" {
		t.Fatalf("admin read should return the order, got %+v", single)
	}
	if single.InternalNote != "call before dispatch" {
		t.Fatalf("admin read should include internal_note, got %q", single.InternalNote)
	}

	// The customer path still refuses a missing phone.
	do("GET", "/api/v1/orders/"+orderID, "", "", fiber.StatusBadRequest)

	// source=draft filter surfaces exactly this draft.
	res = do("GET", "/api/v1/orders?source=draft", a.token, "", fiber.StatusOK)
	var list struct {
		Orders []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		} `json:"orders"`
	}
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode draft list: %v", err)
	}
	if len(list.Orders) != 1 || list.Orders[0].ID != orderID {
		t.Fatalf("source=draft should return exactly the draft, got %+v", list.Orders)
	}

	// status and payment_status filters agree on the pending order.
	res = do("GET", "/api/v1/orders?status=pending&payment_status=pending", a.token, "", fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode status-filtered list: %v", err)
	}
	if len(list.Orders) != 1 || list.Orders[0].ID != orderID {
		t.Fatalf("status+payment_status=pending should return the draft, got %+v", list.Orders)
	}

	// Tenant B never sees it — on the single read or the filtered list.
	res = do("GET", "/api/v1/orders/"+orderID, b.token, "", fiber.StatusNotFound)
	res = do("GET", "/api/v1/orders?source=draft", b.token, "", fiber.StatusOK)
	if err := json.NewDecoder(res.Body).Decode(&list); err != nil {
		t.Fatalf("decode b draft list: %v", err)
	}
	if len(list.Orders) != 0 {
		t.Fatalf("tenant B should see no drafts, got %d", len(list.Orders))
	}
}
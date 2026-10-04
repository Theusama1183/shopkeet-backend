// External test package: exercises the Phase 27 affiliate surface end to end —
// apply → merchant approve → partner login → checkout via ?ref=CODE books a
// pending commission → delivery approves it → payout request + merchant-paid
// flips it to paid. Also asserts the affiliate JWT scope is rejected by both
// merchant-admin and customer-scoped routes. Runs against a real database
// (skipped unless DATABASE_URL is set, mirroring the loyalty acceptance test).
package affiliates_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/affiliates"
	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/customers"
	"github.com/shopkeet/api/internal/orders"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

type applyPayload struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Email  string `json:"email"`
	Code   string `json:"code"`
	Status string `json:"status"`
}

type loginPayload struct {
	Token     string `json:"token"`
	Expires   string `json:"expires"`
	Affiliate *struct {
		ID     string `json:"id"`
		Email  string `json:"email"`
		Status string `json:"status"`
	} `json:"affiliate"`
}

type commission struct {
	ID              string `json:"id"`
	OrderID         string `json:"order_id"`
	CommissionCents int    `json:"commission_cents"`
	Status          string `json:"status"`
}

type commissionsPayload struct {
	Commissions []commission `json:"commissions"`
}

type dashboardPayload struct {
	Totals struct {
		Orders                 int `json:"orders"`
		PendingCents           int `json:"pending_cents"`
		PayoutAvailableCents   int `json:"payout_available_cents"`
		PaidCents              int `json:"paid_cents"`
		OutstandingPayoutCents int `json:"outstanding_payout_cents"`
	} `json:"totals"`
}

type payoutPayload struct {
	ID          string `json:"id"`
	AmountCents int    `json:"amount_cents"`
	Status      string `json:"status"`
}

func TestAffiliateProgram(t *testing.T) {
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
			name, "aff-"+name+"-"+sfx).Scan(&tid)
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

	// seedCatalog creates one active product/variant priced at 2000 in tenant tid.
	seedCatalog := func(tid string) (variantID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var prodID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'usd', $5, 'active') RETURNING id`,
			tid, "aff-prod", "aff-prod-"+sfx+tid[:4], 2000, 10).Scan(&prodID); err != nil {
			t.Fatalf("seed product: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
			VALUES ($1, $2, $3, $4, 'active') RETURNING id`,
			tid, prodID, 2000, 10).Scan(&variantID); err != nil {
			t.Fatalf("seed variant: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		return variantID
	}
	aVariant := seedCatalog(aID)
	seedCatalog(bID)

	seedRate := func(tid string) (rateID string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin zone: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var zoneID string
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipping_zones (tenant_id, name, countries, regions)
			VALUES ($1, 'PK', '{PK}', '{}') RETURNING id`, tid).Scan(&zoneID); err != nil {
			t.Fatalf("seed zone: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, free_over_cents, sort_order)
			VALUES ($1, $2, 'Standard', 500, NULL, 0) RETURNING id`, tid, zoneID).Scan(&rateID); err != nil {
			t.Fatalf("seed rate: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit zone: %v", err)
		}
		return rateID
	}
	aRate := seedRate(aID)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	cart.RegisterRoutes(v1, pool, secret, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	bus := events.NewBus()
	orders.RegisterRoutes(v1, pool, secret, orders.New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))
	customers.RegisterRoutes(v1, pool, secret, customers.New(pool, secret, bus), ratelimit.New(nil))
	affiliatesSvc := affiliates.New(pool, secret)
	affiliatesSvc.Subscribe(bus)
	affiliates.RegisterRoutes(v1, pool, secret, affiliatesSvc, ratelimit.New(nil))

	doTenant := func(tid string) func(method, path, session, bearer, body string, want int) *http.Response {
		return func(method, path, session, bearer, body string, want int) *http.Response {
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
			if res.StatusCode != want {
				body, _ := ioRead(res)
				t.Fatalf("%s %s: status %d, want %d (body=%s)", method, path, res.StatusCode, want, body)
			}
			return res
		}
	}
	doA := doTenant(aID)
	doB := doTenant(bID)

	completeOrder := func(do func(method, path, session, bearer, body string, want int) *http.Response,
		orderID, adminToken string) {
		do("PATCH", "/api/v1/orders/"+orderID+"/status", "", adminToken, `{"status":"confirmed"}`, fiber.StatusOK)
		do("PATCH", "/api/v1/orders/"+orderID+"/status", "", adminToken, `{"status":"shipped"}`, fiber.StatusOK)
		do("PATCH", "/api/v1/orders/"+orderID+"/status", "", adminToken, `{"status":"delivered"}`, fiber.StatusOK)
	}

	// checkout submits the current guest cart with an optional ?ref= affiliate code.
	checkout := func(do func(method, path, session, bearer, body string, want int) *http.Response,
		session, ref string) string {
		path := "/api/v1/checkout"
		if ref != "" {
			path += "?ref=" + ref
		}
		res := do("POST", path, session, "",
			`{"customer_name":"Ada","customer_phone":"+1-555-0001",`+
				`"shipping_address_line1":"2 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
				`"shipping_rate_id":"`+aRate+`"}`, fiber.StatusCreated)
		var ord struct {
			ID string `json:"id"`
		}
		if err := json.NewDecoder(res.Body).Decode(&ord); err != nil || ord.ID == "" {
			t.Fatalf("decode order: %v", err)
		}
		return ord.ID
	}

	getCommissions := func(do func(method, path, session, bearer, body string, want int) *http.Response,
		token string) commissionsPayload {
		res := do("GET", "/api/v1/affiliates/me/commissions", "", token, "", fiber.StatusOK)
		var out commissionsPayload
		if err := json.NewDecoder(res.Body).Decode(&out); err != nil {
			t.Fatalf("decode commissions: %v", err)
		}
		return out
	}

	// --- application → approval → login --------------------------------------

	// A storefront visitor applies in tenant A (pending).
	res := doA("POST", "/api/v1/affiliates/apply", "", "",
		`{"name":"Partner One","email":"partner@example.com","password":"supersecret8"}`, fiber.StatusCreated)
	var af applyPayload
	if err := json.NewDecoder(res.Body).Decode(&af); err != nil {
		t.Fatalf("decode apply: %v", err)
	}
	if af.Status != "pending" || af.Code == "" || af.ID == "" {
		t.Fatalf("expected pending affiliate with code, got %+v", af)
	}

	// Pending affiliate cannot log in yet.
	doA("POST", "/api/v1/affiliates/login", "", "",
		`{"email":"partner@example.com","password":"supersecret8","subdomain":"aff-alpha-`+sfx+`"}`,
		fiber.StatusForbidden)

	// Duplicate apply for the same email → 409.
	doA("POST", "/api/v1/affiliates/apply", "", "",
		`{"name":"Impostor","email":"partner@example.com","password":"supersecret8"}`, fiber.StatusConflict)

	// Merchant approves with a 10% commission.
	res = doA("PATCH", "/api/v1/affiliates/"+af.ID+"/status", "", aAdmin,
		`{"status":"approved","commission_percent":10}`, fiber.StatusOK)
	if body, _ := ioRead(res); !strings.Contains(body, `"status":"approved"`) {
		t.Fatalf("approve response should carry approved status")
	}

	// Now login succeeds with an affiliate-scoped token.
	res = doA("POST", "/api/v1/affiliates/login", "", "",
		`{"email":"partner@example.com","password":"supersecret8","subdomain":"aff-alpha-`+sfx+`"}`,
		fiber.StatusOK)
	var lg loginPayload
	if err := json.NewDecoder(res.Body).Decode(&lg); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if lg.Affiliate.Status != "approved" || lg.Token == "" {
		t.Fatalf("expected approved affiliate + token, got %+v", lg)
	}
	afToken := lg.Token

	// --- an invalid / inactive link never interferes with checkout ------------

	// Guest adds a unit to a fresh cart, checks out with a bogus ref.
	s0 := "aff-s0-" + sfx
	doA("POST", "/api/v1/cart", s0, "", `{"variant_id":"`+aVariant+`","quantity":1}`, fiber.StatusOK)
	checkout(doA, s0, "NOPE")
	if got := getCommissions(doA, afToken); len(got.Commissions) != 0 {
		t.Fatalf("bogus ref must not book a commission, got %+v", got.Commissions)
	}

	// --- checkout via a valid link books a pending commission ------------------

	s1 := "aff-s1-" + sfx
	doA("POST", "/api/v1/cart", s1, "", `{"variant_id":"`+aVariant+`","quantity":1}`, fiber.StatusOK)
	ord1 := checkout(doA, s1, af.Code)

	got := getCommissions(doA, afToken)
	if len(got.Commissions) != 1 {
		t.Fatalf("expected one pending commission, got %+v", got.Commissions)
	}
	comm := got.Commissions[0]
	// 10% of the 2000-cent goods subtotal (no discounts/shipping/tax excluded).
	if comm.CommissionCents != 200 || comm.Status != "pending" || comm.OrderID != ord1 {
		t.Fatalf("expected pending 200-cent commission on %s, got %+v", ord1, comm)
	}

	// --- approval on delivery -------------------------------------------------

	completeOrder(doA, ord1, aAdmin)

	got = getCommissions(doA, afToken)
	if len(got.Commissions) != 1 || got.Commissions[0].Status != "approved" {
		t.Fatalf("expected commission approved after delivery, got %+v", got.Commissions)
	}

	// Re-emitting order.paid must not break the (already approved) state.
	bus.Emit(ctx, events.Event{Name: "order.paid",
		Data: fiber.Map{"order_id": ord1, "tenant_id": aID}})

	// --- payout request + merchant-paid ---------------------------------------

	res = doA("POST", "/api/v1/affiliates/me/payout-request", "", afToken, "", fiber.StatusCreated)
	var payout payoutPayload
	if err := json.NewDecoder(res.Body).Decode(&payout); err != nil {
		t.Fatalf("decode payout: %v", err)
	}
	if payout.AmountCents != 200 || payout.Status != "requested" {
		t.Fatalf("expected 200-cent requested payout, got %+v", payout)
	}

	// A second request while one is outstanding → 409.
	doA("POST", "/api/v1/affiliates/me/payout-request", "", afToken, "", fiber.StatusConflict)

	// Merchant pays it; commissions flip to paid.
	doA("PATCH", "/api/v1/affiliate-payouts/"+payout.ID, "", aAdmin, `{"status":"paid"}`, fiber.StatusOK)

	got = getCommissions(doA, afToken)
	if len(got.Commissions) != 1 || got.Commissions[0].Status != "paid" {
		t.Fatalf("expected commission paid after payout settled, got %+v", got.Commissions)
	}

	res = doA("GET", "/api/v1/affiliates/me/dashboard", "", afToken, "", fiber.StatusOK)
	var dash dashboardPayload
	if err := json.NewDecoder(res.Body).Decode(&dash); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if dash.Totals.Orders != 1 || dash.Totals.PaidCents != 200 ||
		dash.Totals.PayoutAvailableCents != 0 || dash.Totals.PendingCents != 0 {
		t.Fatalf("unexpected dashboard totals after paid payout: %+v", dash.Totals)
	}

	// --- the affiliate scope is refused by merchant + customer routes ---------

	// Merchant admin route (TenantMW refuses non-merchant scopes → 403).
	doA("GET", "/api/v1/affiliates", "", afToken, "", fiber.StatusForbidden)
	// Customer-scoped route (CustomerAuthMW refuses non-customer scopes → 403).
	doA("GET", "/api/v1/customers/me/orders", "", afToken, "", fiber.StatusForbidden)

	// --- tenant B cannot see or touch tenant A's payouts ----------------------

	bAdmin, _ := auth.Sign(secret, bID, bID[:8], "owner", time.Hour)
	doB("GET", "/api/v1/affiliates", "", bAdmin, "", fiber.StatusOK)
	doB("PATCH", "/api/v1/affiliate-payouts/"+payout.ID, "", bAdmin, `{"status":"paid"}`,
		fiber.StatusNotFound)
}

func ioRead(res *http.Response) (string, error) {
	b := make([]byte, 0, 4096)
	buf := make([]byte, 1024)
	for {
		n, err := res.Body.Read(buf)
		b = append(b, buf[:n]...)
		if err != nil {
			break
		}
	}
	return string(b), nil
}
// External test package: it mounts the customer surface plus the cart/orders
// routes checkout relies on, so it must live outside package customers to keep
// imports one-directional.
package customers_test

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

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/customers"
	"github.com/shopkeet/api/internal/orders"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

type customerPayload struct {
	Token    string `json:"token"`
	Expires  string `json:"expires"`
	Customer *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
		Phone string `json:"phone"`
	} `json:"customer"`
}

type profilePayload struct {
	ID        string `json:"id"`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	Addresses []struct {
		ID        string `json:"id"`
		Label     string `json:"label"`
		Line1     string `json:"address_line1"`
		City      string `json:"city"`
		Country   string `json:"country"`
		IsDefault bool   `json:"is_default"`
	} `json:"addresses"`
}

type orderPayload struct {
	ID            string `json:"id"`
	CustomerID    string `json:"customer_id"`
	TotalCents    int    `json:"total_cents"`
	Status        string `json:"status"`
	PaymentStatus string `json:"payment_status"`
}

type ordersListPayload struct {
	Orders []orderPayload `json:"orders"`
}

// TestCustomersRLSIsolation is the Phase 11 criterion: a customer registers,
// logs in and sees their past orders; guest checkout keeps working with
// customer_id NULL; customer JWTs never reach admin routes and merchant JWTs
// never reach customer routes; addresses CRUD + cross-tenant isolation hold.
func TestCustomersRLSIsolation(t *testing.T) {
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
			name, "cust-"+name+"-"+sfx).Scan(&tid)
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

	seedCatalog := func(tid string) (prodID, variantID string) {
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
			tid, "cust-prod", "cust-prod-"+sfx+tid[:4], 2000, 10).Scan(&prodID); err != nil {
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
		return prodID, variantID
	}
	aProd, aVariant := seedCatalog(aID)

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
	cart.RegisterRoutes(v1, pool, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	bus := events.NewBus()
	orders.RegisterRoutes(v1, pool, secret, orders.New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))
	customers.RegisterRoutes(v1, pool, secret, customers.New(pool, secret, bus), ratelimit.New(nil))

	// doTenant issues a request for a given tenant, with optional session and
	// bearer auth headers.
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

	// --- signup + login -------------------------------------------------------

	signup := `{"email":"ada@example.com","phone":"+1-555-0001","password":"supersecret8"}`
	res := doA("POST", "/api/v1/customers/signup", "", "", signup, fiber.StatusCreated)
	var acct customerPayload
	if err := json.NewDecoder(res.Body).Decode(&acct); err != nil {
		t.Fatalf("decode signup: %v", err)
	}
	ctok := acct.Token
	if ctok == "" || acct.Customer == nil || acct.Customer.ID == "" {
		t.Fatalf("signup should mint a token + customer, got %+v", acct)
	}
	if acct.Customer.Email != "ada@example.com" || acct.Customer.Phone != "+1-555-0001" {
		t.Fatalf("signup profile mismatch: %+v", acct.Customer)
	}

	// Duplicate email in the same tenant -> 409.
	doA("POST", "/api/v1/customers/signup", "", "", signup, fiber.StatusConflict)

	// Wrong password -> 401; correct -> 200.
	doA("POST", "/api/v1/customers/login", "", "",
		`{"email":"ada@example.com","password":"wrong"}`, fiber.StatusUnauthorized)
	res = doA("POST", "/api/v1/customers/login", "", "",
		`{"email":"ada@example.com","password":"supersecret8"}`, fiber.StatusOK)
	var login customerPayload
	if err := json.NewDecoder(res.Body).Decode(&login); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if login.Token == "" {
		t.Fatal("login should return a token")
	}

	// me reflects the profile.
	res = doA("GET", "/api/v1/customers/me", "", ctok, "", fiber.StatusOK)
	var prof profilePayload
	if err := json.NewDecoder(res.Body).Decode(&prof); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if prof.Email != "ada@example.com" || prof.Phone != "+1-555-0001" {
		t.Fatalf("me profile mismatch: %+v", prof)
	}

	// --- guest checkout leaves customer_id NULL; signed-in links the order -------

	// Guest 1: full checkout without any customer token.
	g1 := "sess-guest-1-" + sfx
	doA("POST", "/api/v1/cart", g1, "", `{"variant_id":"`+aVariant+`","quantity":1}`, fiber.StatusOK)
	doA("POST", "/api/v1/checkout", g1, "",
		`{"customer_name":"Guess","customer_phone":"+1-555-7777",`+
			`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+aRate+`"}`,
		fiber.StatusCreated)

	// Signed-in customer: same cart/session flow, but checkout carries the
	// customer JWT, so the order is linked via customer_id.
	c1 := "sess-cust-1-" + sfx
	doA("POST", "/api/v1/cart", c1, "", `{"variant_id":"`+aVariant+`","quantity":1}`, fiber.StatusOK)
	res = doA("POST", "/api/v1/checkout", c1, ctok,
		`{"customer_name":"Ada","customer_phone":"+1-555-0001",`+
			`"shipping_address_line1":"2 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
			`"shipping_rate_id":"`+aRate+`"}`,
		fiber.StatusCreated)
	var linked orderPayload
	if err := json.NewDecoder(res.Body).Decode(&linked); err != nil {
		t.Fatalf("decode linked order: %v", err)
	}
	if linked.CustomerID != acct.Customer.ID {
		t.Fatalf("signed-in order should carry customer_id=%s, got %q",
			acct.Customer.ID, linked.CustomerID)
	}
	if linked.TotalCents != 2500 { // 2000 + 500 shipping
		t.Fatalf("expected total 2500, got %d", linked.TotalCents)
	}

	// me/orders: exactly the linked order — the guest order stays invisible.
	res = doA("GET", "/api/v1/customers/me/orders", "", ctok, "", fiber.StatusOK)
	var hist ordersListPayload
	if err := json.NewDecoder(res.Body).Decode(&hist); err != nil {
		t.Fatalf("decode me/orders: %v", err)
	}
	if len(hist.Orders) != 1 || hist.Orders[0].ID != linked.ID {
		t.Fatalf("me/orders should show only the linked order, got %+v", hist.Orders)
	}

	// The guest order really is NULL — confirm via the admin list.
	res = doA("GET", "/api/v1/orders", "", aAdmin, "", fiber.StatusOK)
	var adminList struct {
		Orders []orderPayload `json:"orders"`
	}
	if err := json.NewDecoder(res.Body).Decode(&adminList); err != nil {
		t.Fatalf("decode admin orders: %v", err)
	}
	if len(adminList.Orders) != 2 {
		t.Fatalf("expected 2 orders in admin list, got %d", len(adminList.Orders))
	}
	guestID := ""
	for _, o := range adminList.Orders {
		if o.ID != linked.ID {
			guestID = o.ID
			if o.CustomerID != "" {
				t.Fatalf("guest order should have empty customer_id, got %q", o.CustomerID)
			}
		}
	}
	if guestID == "" {
		t.Fatal("guest order not found in admin list")
	}
	_ = aProd

	// --- addresses ---------------------------------------------------------------

	res = doA("POST", "/api/v1/customers/me/addresses", "", ctok,
		`{"label":"Home","address_line1":"10 A Street","city":"Karachi","state":"SD","country":"PK","is_default":true}`,
		fiber.StatusCreated)
	var addr1 struct {
		Address struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"is_default"`
		} `json:"address"`
	}
	if err := json.NewDecoder(res.Body).Decode(&addr1); err != nil {
		t.Fatalf("decode addr1: %v", err)
	}
	if !addr1.Address.IsDefault {
		t.Fatal("home address should be default")
	}

	// Promoting a second address to default demotes the first.
	res = doA("POST", "/api/v1/customers/me/addresses", "", ctok,
		`{"label":"Office","address_line1":"20 B Street","city":"Lahore","country":"PK"}`, fiber.StatusCreated)
	var addr2 struct {
		Address struct {
			ID        string `json:"id"`
			IsDefault bool   `json:"is_default"`
		} `json:"address"`
	}
	if err := json.NewDecoder(res.Body).Decode(&addr2); err != nil {
		t.Fatalf("decode addr2: %v", err)
	}
	if addr2.Address.IsDefault {
		t.Fatal("second address should not start default")
	}
	res = doA("PATCH", "/api/v1/customers/me/addresses/"+addr2.Address.ID, "", ctok,
		`{"is_default":true}`, fiber.StatusOK)

	res = doA("GET", "/api/v1/customers/me", "", ctok, "", fiber.StatusOK)
	prof = profilePayload{}
	if err := json.NewDecoder(res.Body).Decode(&prof); err != nil {
		t.Fatalf("decode me: %v", err)
	}
	if len(prof.Addresses) != 2 {
		t.Fatalf("expected 2 addresses, got %d", len(prof.Addresses))
	}
	for _, a := range prof.Addresses {
		if a.ID == addr1.Address.ID && a.IsDefault {
			t.Fatal("demoted address should not be default")
		}
		if a.ID == addr2.Address.ID && !a.IsDefault {
			t.Fatal("the second address should now be default")
		}
	}

	doA("DELETE", "/api/v1/customers/me/addresses/"+addr1.Address.ID, "", ctok, "", fiber.StatusNoContent)
	doA("DELETE", "/api/v1/customers/me/addresses/"+addr1.Address.ID, "", ctok, "", fiber.StatusNotFound)

	// --- scope isolation ---------------------------------------------------------

	// A customer JWT must never reach an Admin route (TenantMW refuses it).
	doA("GET", "/api/v1/orders", "", ctok, "", fiber.StatusForbidden)
	// A merchant JWT must never reach a customer route (CustomerAuthMW refuses it).
	doA("GET", "/api/v1/customers/me", "", aAdmin, "", fiber.StatusForbidden)
	// No token at all -> 401.
	doA("GET", "/api/v1/customers/me", "", "", "", fiber.StatusUnauthorized)

	// Cross-tenant: the same email on tenant B is a brand-new customer.
	res = doB("POST", "/api/v1/customers/signup", "", "", signup, fiber.StatusCreated)
	acctB := customerPayload{}
	if err := json.NewDecoder(res.Body).Decode(&acctB); err != nil {
		t.Fatalf("decode beta signup: %v", err)
	}
	if acctB.Customer.ID == acct.Customer.ID {
		t.Fatal("tenant B signup must create a distinct customer")
	}
	// B's customer sees no orders from tenant A.
	res = doB("GET", "/api/v1/customers/me/orders", "", acctB.Token, "", fiber.StatusOK)
	hist = ordersListPayload{}
	if err := json.NewDecoder(res.Body).Decode(&hist); err != nil {
		t.Fatalf("decode beta orders: %v", err)
	}
	if len(hist.Orders) != 0 {
		t.Fatalf("tenant B customer should see zero orders, got %+v", hist.Orders)
	}
}

func ioRead(res *http.Response) (string, error) {
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			if err.Error() == "EOF" {
				return sb.String(), nil
			}
			return sb.String(), err
		}
	}
}

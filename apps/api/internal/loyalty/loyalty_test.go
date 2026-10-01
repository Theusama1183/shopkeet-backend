// External test package: mounts the loyalty surface plus the cart/orders/
// customers routes the acceptance flow relies on, outside package loyalty so
// imports stay one-directional.
package loyalty_test

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
	"github.com/shopkeet/api/internal/loyalty"
	"github.com/shopkeet/api/internal/orders"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

type customerPayload struct {
	Token    string `json:"token"`
	Customer *struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"customer"`
}

type loyaltyPayload struct {
	Balance int `json:"balance"`
	Ledger  []struct {
		ID        string `json:"id"`
		Points    int    `json:"points"`
		Reason    string `json:"reason"`
		OrderID   string `json:"order_id"`
		CreatedAt string `json:"created_at"`
	} `json:"ledger"`
}

type redeemPayload struct {
	Code          string `json:"code"`
	DiscountCents int    `json:"discount_cents"`
	Points        int    `json:"points"`
	Balance       int    `json:"balance"`
}

func TestLoyaltyReferrals(t *testing.T) {
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

	// mkTenant seeds a tenant with the Phase 20 program enabled: 10 points per
	// currency unit earned, 100 points = 1 currency unit of discount.
	mkTenant := func(name string) (tid, adminToken string) {
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "loy-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		if _, err := pool.Exec(ctx, `
			UPDATE tenants SET loyalty_points_per_currency_unit = 10,
			                   loyalty_redemption_rate = 100
			WHERE id = $1`, tid); err != nil {
			t.Fatalf("enable loyalty %s: %v", name, err)
		}
		adminToken, err = auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
		if err != nil {
			t.Fatalf("sign: %v", err)
		}
		return tid, adminToken
	}
	aID, aAdmin := mkTenant("alpha")
	bID, _ := mkTenant("beta")

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
			tid, "loy-prod", "loy-prod-"+sfx+tid[:4], 2000, 10).Scan(&prodID); err != nil {
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
	loyaltySvc := loyalty.New(pool)
	loyaltySvc.Subscribe(bus)
	loyalty.RegisterRoutes(v1, pool, secret, loyaltySvc)

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

	checkout := func(do func(method, path, session, bearer, body string, want int) *http.Response,
		session, token string) string {
		res := do("POST", "/api/v1/checkout", session, token,
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

	// --- signup ---------------------------------------------------------------

	res := doA("POST", "/api/v1/customers/signup", "", "",
		`{"email":"ada@example.com","phone":"+1-555-0001","password":"supersecret8"}`, fiber.StatusCreated)
	var ada customerPayload
	if err := json.NewDecoder(res.Body).Decode(&ada); err != nil {
		t.Fatalf("decode ada signup: %v", err)
	}

	// Fresh account: zero balance, empty ledger.
	res = doA("GET", "/api/v1/customers/me/loyalty", "", ada.Token, "", fiber.StatusOK)
	var loy loyaltyPayload
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode loyalty: %v", err)
	}
	if loy.Balance != 0 || len(loy.Ledger) != 0 {
		t.Fatalf("fresh customer should have 0 balance + empty ledger, got %+v", loy)
	}

	// --- points on delivery ---------------------------------------------------

	s1 := "loy-s1-" + sfx
	doA("POST", "/api/v1/cart", s1, "", `{"variant_id":"`+aVariant+`","quantity":1}`, fiber.StatusOK)
	ord1 := checkout(doA, s1, ada.Token)
	completeOrder(doA, ord1, aAdmin)

	res = doA("GET", "/api/v1/customers/me/loyalty", "", ada.Token, "", fiber.StatusOK)
	loy = loyaltyPayload{}
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode loyalty: %v", err)
	}
	// 2500 cents total / 100 = 25 units x 10 points = 250.
	if loy.Balance != 250 {
		t.Fatalf("expected balance 250 after delivered order, got %d", loy.Balance)
	}
	if len(loy.Ledger) != 1 || loy.Ledger[0].Reason != "order_placed" || loy.Ledger[0].Points != 250 {
		t.Fatalf("expected one order_placed +250 entry, got %+v", loy.Ledger)
	}

	// Re-emitting order.paid for the same order must not double-credit
	// (partial unique index per order+reason + ON CONFLICT DO NOTHING).
	bus.Emit(ctx, events.Event{Name: "order.paid",
		Data: fiber.Map{"order_id": ord1, "tenant_id": aID}})
	res = doA("GET", "/api/v1/customers/me/loyalty", "", ada.Token, "", fiber.StatusOK)
	loy = loyaltyPayload{}
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode loyalty: %v", err)
	}
	if loy.Balance != 250 || len(loy.Ledger) != 1 {
		t.Fatalf("re-emitted order.paid must not double-credit, got %+v", loy)
	}

	// --- redemption -----------------------------------------------------------

	// Redeeming more than the balance -> 400 (the Phase 20 acceptance rule).
	doA("POST", "/api/v1/loyalty/redeem", "", ada.Token, `{"points":251}`, fiber.StatusBadRequest)

	// 150 points at 100/unit -> 1 full unit -> 100 cents.
	res = doA("POST", "/api/v1/loyalty/redeem", "", ada.Token, `{"points":150}`, fiber.StatusOK)
	var red redeemPayload
	if err := json.NewDecoder(res.Body).Decode(&red); err != nil {
		t.Fatalf("decode redeem: %v", err)
	}
	if red.Code == "" || red.DiscountCents != 100 || red.Points != 150 || red.Balance != 100 {
		t.Fatalf("unexpected redeem result: %+v", red)
	}

	// Idempotency: a replayed redeem (same key) must not spend points again.
	newRedeem := func(key string) *http.Response {
		req := httptest.NewRequest("POST", "/api/v1/loyalty/redeem", strings.NewReader(`{"points":100}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant-ID", aID)
		req.Header.Set("Authorization", "Bearer "+ada.Token)
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("redeem replay: %v", err)
		}
		return res
	}
	key := "redeem-1-" + sfx
	r1 := newRedeem(key)
	b1, _ := ioRead(r1)
	r2 := newRedeem(key)
	b2, _ := ioRead(r2)
	if r1.StatusCode != fiber.StatusOK || r2.StatusCode != fiber.StatusOK || b1 != b2 {
		t.Fatalf("idempotent redeem mismatch: %d vs %d (%s vs %s)", r1.StatusCode, r2.StatusCode, b1, b2)
	}

	// Balance still 100 (one redemption of 150 only).
	res = doA("GET", "/api/v1/customers/me/loyalty", "", ada.Token, "", fiber.StatusOK)
	loy = loyaltyPayload{}
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode loyalty: %v", err)
	}
	if loy.Balance != 100 {
		t.Fatalf("expected balance 100 after redemptions, got %d", loy.Balance)
	}

	// Over-balance -> 400.
	doA("POST", "/api/v1/loyalty/redeem", "", ada.Token, `{"points":101}`, fiber.StatusBadRequest)
	// Non-whole unit (50 < rate 100) -> 400.
	doA("POST", "/api/v1/loyalty/redeem", "", ada.Token, `{"points":50}`, fiber.StatusBadRequest)

	// Redemption disabled (rate 0) -> 400, then re-enable.
	if _, err := pool.Exec(ctx,
		"UPDATE tenants SET loyalty_redemption_rate = 0 WHERE id = $1", aID); err != nil {
		t.Fatalf("disable redemption: %v", err)
	}
	doA("POST", "/api/v1/loyalty/redeem", "", ada.Token, `{"points":100}`, fiber.StatusBadRequest)
	if _, err := pool.Exec(ctx,
		"UPDATE tenants SET loyalty_redemption_rate = 100 WHERE id = $1", aID); err != nil {
		t.Fatalf("re-enable redemption: %v", err)
	}

	// --- referral code ----------------------------------------------------------

	res = doA("GET", "/api/v1/customers/me/referral", "", ada.Token, "", fiber.StatusOK)
	var ref struct {
		Code       string `json:"code"`
		ValueCents int    `json:"value_cents"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ref); err != nil {
		t.Fatalf("decode referral: %v", err)
	}
	if ref.Code == "" || ref.ValueCents != 500 {
		t.Fatalf("unexpected referral: %+v", ref)
	}
	// Re-request returns the same code (one per customer).
	doA("GET", "/api/v1/customers/me/referral", "", ada.Token, "", fiber.StatusOK)

	// --- referral payout ---------------------------------------------------------

	res = doB("POST", "/api/v1/customers/signup", "", "",
		`{"email":"bob@example.com","phone":"+1-555-0002","password":"supersecret8"}`, fiber.StatusCreated)
	var bob customerPayload
	if err := json.NewDecoder(res.Body).Decode(&bob); err != nil {
		t.Fatalf("decode bob signup: %v", err)
	}

	s2 := "loy-s2-" + sfx
	doA("POST", "/api/v1/cart", s2, "", `{"variant_id":"`+aVariant+`","quantity":1}`, fiber.StatusOK)
	doA("POST", "/api/v1/cart/discount", s2, "", `{"code":"`+ref.Code+`"}`, fiber.StatusOK)
	ord2 := checkout(doA, s2, bob.Token)
	completeOrder(doA, ord2, aAdmin)
	// Bob's total after the -500 referral discount = 2000; 20 units x 10 = 200.
	res = doA("GET", "/api/v1/customers/me/loyalty", s2, bob.Token, "", fiber.StatusOK)
	loy = loyaltyPayload{}
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode bob loyalty: %v", err)
	}
	if loy.Balance != 200 {
		t.Fatalf("expected bob balance 200, got %d", loy.Balance)
	}
	// Ada earned the same 200 as the referrer.
	res = doA("GET", "/api/v1/customers/me/loyalty", "", ada.Token, "", fiber.StatusOK)
	loy = loyaltyPayload{}
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode ada loyalty: %v", err)
	}
	if loy.Balance != 100+200 {
		t.Fatalf("expected ada balance 300 after referral payout, got %d", loy.Balance)
	}
	foundReferral := false
	for _, e := range loy.Ledger {
		if e.Reason == "referral" {
			foundReferral = true
			if e.Points != 200 {
				t.Fatalf("expected referral payout 200, got %d", e.Points)
			}
		}
	}
	if !foundReferral {
		t.Fatalf("expected a referral entry in ada's ledger, got %+v", loy.Ledger)
	}

	// --- self-referral earns no payout ------------------------------------------

	s3 := "loy-s3-" + sfx
	doA("POST", "/api/v1/cart", s3, "", `{"variant_id":"`+aVariant+`","quantity":1}`, fiber.StatusOK)
	doA("POST", "/api/v1/cart/discount", s3, "", `{"code":"`+ref.Code+`"}`, fiber.StatusOK)
	ord3 := checkout(doA, s3, ada.Token)
	completeOrder(doA, ord3, aAdmin)

	// Ada used her own code -> the order must have zero referral rows. Check
	// the ledger inside a scoped tx (bypassing the request middlewares).
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", aID); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var selfRef int
	if err := tx.QueryRow(ctx,
		"SELECT count(*) FROM loyalty_ledger WHERE order_id = $1 AND reason = 'referral'",
		ord3).Scan(&selfRef); err != nil {
		t.Fatalf("count self referral: %v", err)
	}
	if selfRef != 0 {
		t.Fatalf("self-referral must not pay out, found %d referral rows for order %s", selfRef, ord3)
	}

	// Ada still earned her own order_placed credit for ord3 (2000 -> +200).
	res = doA("GET", "/api/v1/customers/me/loyalty", "", ada.Token, "", fiber.StatusOK)
	loy = loyaltyPayload{}
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode ada loyalty: %v", err)
	}
	if loy.Balance != 500 {
		t.Fatalf("expected ada balance 500 after self order, got %d", loy.Balance)
	}

	// --- cross-tenant isolation ---------------------------------------------------

	// A brand-new beta customer sees zero balance and their own separate code.
	res = doB("GET", "/api/v1/customers/me/loyalty", "", bob.Token, "", fiber.StatusOK)
	loy = loyaltyPayload{}
	if err := json.NewDecoder(res.Body).Decode(&loy); err != nil {
		t.Fatalf("decode beta loyalty: %v", err)
	}
	if loy.Balance != 0 || len(loy.Ledger) != 0 {
		t.Fatalf("beta customer should see zero loyalty, got %+v", loy)
	}

	// Admin settings surface exposes the Phase 20 knobs.
	res = doA("GET", "/api/v1/tenant/settings", "", aAdmin, "", fiber.StatusOK)
	var st struct {
		Settings struct {
			LoyaltyPointsPerCurrencyUnit int `json:"loyalty_points_per_currency_unit"`
			LoyaltyRedemptionRate        int `json:"loyalty_redemption_rate"`
		} `json:"settings"`
	}
	if err := json.NewDecoder(res.Body).Decode(&st); err != nil {
		t.Fatalf("decode settings: %v", err)
	}
	if st.Settings.LoyaltyPointsPerCurrencyUnit != 10 || st.Settings.LoyaltyRedemptionRate != 100 {
		t.Fatalf("settings should echo loyalty config, got %+v", st.Settings)
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

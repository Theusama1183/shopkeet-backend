package orders

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// TestInvoiceTotalsMath is the pure half of the Phase 23 acceptance: the PDF's
// money block derives the total from exactly the same columns checkout snapshots
// (subtotal + shipping + tax − discount − gift), so it can never disagree with
// orders.total_cents. Runs without a database.
func TestInvoiceTotalsMath(t *testing.T) {
	o := &orderRow{
		shippingCostCents: 500,
		discountCents:     100,
		giftCardCents:     250,
		taxCents:          25,
		totalCents:        2175, // 2000 + 500 + 25 − 100 − 250
		currency:          "usd",
		customerName:      "Ada",
		customerPhone:     "+1-555-0001",
	}
	lines := []invoiceLine{
		{name: "Mug", sku: "MUG-1", qty: 2, unit: 700, line: 1400},
		{name: "Café Teapot", sku: "TEA-2", qty: 1, unit: 600, line: 600},
	}
	tot := computeInvoice(o, lines)
	if tot.subtotal != 2000 || tot.shipping != 500 || tot.discount != 100 ||
		tot.gift != 250 || tot.tax != 25 || tot.total != o.totalCents {
		t.Fatalf("invoice totals disagree with order: got %+v want total %d", tot, o.totalCents)
	}

	pdf, err := renderInvoice(o, lines, "Kitchen Shop")
	if err != nil {
		t.Fatalf("renderInvoice: %v", err)
	}
	if len(pdf) == 0 || !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Fatalf("renderInvoice did not produce a PDF, first bytes=%q", pdf[:min(8, len(pdf))])
	}
	if got := money("usd", 2175); got != "$21.75" {
		t.Fatalf("money(usd) = %q, want $21.75", got)
	}
	if got := lat1("Café 🐝"); got != "Café ?" {
		t.Fatalf("lat1 mapping wrong: %q", got)
	}
}

// TestOrderTrackingAndInvoicePDF is the integration half of the Phase 23
// acceptance: an order advanced to shipped with tracking info returns it in the
// customer-facing lookup, the tracking survives later transitions, and both the
// admin and customer invoice routes stream back a real PDF. Requires DATABASE_URL.
func TestOrderTrackingAndInvoicePDF(t *testing.T) {
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

	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"tracking", "trk-"+sfx).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	adminToken, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	var prodID, variantID string
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
		VALUES ($1, $2, $3, 2000, 'usd', 5, 'active') RETURNING id`,
		tid, "Invoice Mug", "inv-mug-"+sfx).Scan(&prodID); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, sku, price_cents, inventory_count, status)
		VALUES ($1, $2, $3, 2000, 5, 'active') RETURNING id`,
		tid, prodID, "INV-MUG").Scan(&variantID); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	var zoneID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_zones (tenant_id, name, countries, regions)
		VALUES ($1, 'PK', '{PK}', '{}') RETURNING id`, tid).Scan(&zoneID); err != nil {
		t.Fatalf("seed zone: %v", err)
	}
	var rateID string
	if err := tx.QueryRow(ctx, `
		INSERT INTO shipping_rates (tenant_id, zone_id, name, rate_cents, sort_order)
		VALUES ($1, $2, 'Standard', 500, 0) RETURNING id`, tid, zoneID).Scan(&rateID); err != nil {
		t.Fatalf("seed rate: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	bus := events.NewBus()
	cart.RegisterRoutes(v1, pool, secret, cart.New(pool, cart.NoopReserver{}), ratelimit.New(nil))
	RegisterRoutes(v1, pool, secret, New(pool, bus, payments.NewRegistry()), ratelimit.New(nil))

	do := func(method, path, session, bearer, body string, want int) *http.Response {
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

	phone := "+1-555-" + sfx
	// The phone must be query-escaped: a raw "+" decodes to a space and would
	// 404 the customer lookup (same convention as orders_test.go).
	encPhone := url.QueryEscape(phone)
	session := "sess-" + sfx
	do("POST", "/api/v1/cart", session, "", `{"variant_id":"`+variantID+`","quantity":1}`, fiber.StatusOK)
	res := do("POST", "/api/v1/checkout", session, "", `{"customer_name":"Ada",`+
		`"customer_phone":"`+phone+`","customer_email":"ada@example.com",`+
		`"shipping_address_line1":"1 Main St","shipping_city":"Lahore","shipping_country":"PK",`+
		`"shipping_rate_id":"`+rateID+`"}`,
		fiber.StatusCreated)
	var ord struct {
		ID         string `json:"id"`
		TotalCents int    `json:"total_cents"`
	}
	if err := json.NewDecoder(res.Body).Decode(&ord); err != nil {
		t.Fatalf("decode checkout: %v", err)
	}
	if ord.ID == "" || ord.TotalCents != 2500 {
		t.Fatalf("checkout gave order %q total %d, want 2500", ord.ID, ord.TotalCents)
	}

	// Checkout lands on 'pending'; walk to confirmed, then advance to shipped
	// and attach tracking (Phase 23).
	res = do("PATCH", "/api/v1/orders/"+ord.ID+"/status", "", adminToken,
		`{"status":"confirmed"}`, fiber.StatusOK)
	res = do("PATCH", "/api/v1/orders/"+ord.ID+"/status", "", adminToken,
		`{"status":"shipped","tracking_number":"TRK123","tracking_carrier":"DHL",`+
			`"tracking_url":"https://dhl.com/TRK123"}`,
		fiber.StatusOK)
	var patched struct {
		Status          string  `json:"status"`
		TrackingNumber  *string `json:"tracking_number"`
		TrackingCarrier *string `json:"tracking_carrier"`
		TrackingURL     *string `json:"tracking_url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&patched); err != nil {
		t.Fatalf("decode patched order: %v", err)
	}
	if patched.Status != "shipped" || patched.TrackingNumber == nil || *patched.TrackingNumber != "TRK123" ||
		patched.TrackingCarrier == nil || *patched.TrackingCarrier != "DHL" ||
		patched.TrackingURL == nil || *patched.TrackingURL != "https://dhl.com/TRK123" {
		t.Fatalf("tracking not persisted on shipped: %+v", patched)
	}

	// Customer-facing lookup returns the tracking fields.
	res = do("GET", "/api/v1/orders/"+ord.ID+"?phone="+encPhone, "", "", "", fiber.StatusOK)
	var looked struct {
		Status          string `json:"status"`
		TrackingNumber  string `json:"tracking_number"`
		TrackingCarrier string `json:"tracking_carrier"`
		TrackingURL     string `json:"tracking_url"`
	}
	if err := json.NewDecoder(res.Body).Decode(&looked); err != nil {
		t.Fatalf("decode lookup: %v", err)
	}
	if looked.TrackingNumber != "TRK123" || looked.TrackingCarrier != "DHL" || looked.TrackingURL != "https://dhl.com/TRK123" {
		t.Fatalf("customer lookup missing tracking: %+v", looked)
	}

	// Wrong phone sees nothing.
	do("GET", "/api/v1/orders/"+ord.ID+"?phone="+url.QueryEscape("+1-000-000-0000"), "", "", "", fiber.StatusNotFound)

	// Absent tracking fields are preserved on later transitions.
	do("PATCH", "/api/v1/orders/"+ord.ID+"/status", "", adminToken,
		`{"status":"delivered"}`, fiber.StatusOK)
	res = do("GET", "/api/v1/orders/"+ord.ID+"?phone="+encPhone, "", "", "", fiber.StatusOK)
	var after struct {
		Status          string `json:"status"`
		TrackingNumber  string `json:"tracking_number"`
		TrackingCarrier string `json:"tracking_carrier"`
	}
	if err := json.NewDecoder(res.Body).Decode(&after); err != nil {
		t.Fatalf("decode after-delivered: %v", err)
	}
	if after.Status != "delivered" || after.TrackingNumber != "TRK123" || after.TrackingCarrier != "DHL" {
		t.Fatalf("tracking lost after further transition: %+v", after)
	}

	// Invoice PDF via BOTH access paths.
	for _, tc := range []struct {
		name   string
		path   string
		bearer string
	}{
		{"admin", "/api/v1/orders/" + ord.ID + "/invoice.pdf", adminToken},
		{"customer", "/api/v1/orders/" + ord.ID + "/invoice.pdf?phone=" + encPhone, ""},
	} {
		res = do("GET", tc.path, "", tc.bearer, "", fiber.StatusOK)
		if ct := res.Header.Get("Content-Type"); ct != "application/pdf" {
			t.Fatalf("%s: content-type %q, want application/pdf", tc.name, ct)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if len(raw) == 0 || !strings.HasPrefix(string(raw), "%PDF-") {
			t.Fatalf("%s: not a PDF (%d bytes)", tc.name, len(raw))
		}
	}

	// Customer invoice with the wrong phone → 404.
	do("GET", "/api/v1/orders/"+ord.ID+"/invoice.pdf?phone=+1-000-000-0000",
		"", "", "", fiber.StatusNotFound)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

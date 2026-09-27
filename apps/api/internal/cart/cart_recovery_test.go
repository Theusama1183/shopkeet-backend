package cart

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

	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// TestCartRecoverySweep is the Phase 17 acceptance criterion. A cart with a
// captured email that sits idle >1h receives exactly one recovery email and is
// stamped recovery_sent_at (a second sweep sends nothing). Carts that convert
// (checked out — checkout deletes the cart), carry no email, hold no items, or
// are still fresh (<1h since last activity) never receive one. last_activity_at
// refreshes on every cart mutation (add/patch/delete/discount/email).
func TestCartRecoverySweep(t *testing.T) {
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
	tid := mkTenantID(t, pool, "recover-alpha-"+sfx)

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	RegisterRoutes(app.Group("/api/v1"), pool, New(pool, NoopReserver{}), ratelimit.New(nil))

	cartIDOf := func(session string) string {
		t.Helper()
		req := httptest.NewRequest("GET", "/api/v1/cart", nil)
		req.Header.Set("X-Tenant-ID", tid)
		req.Header.Set("X-Customer-Session", session)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("get cart: %v", err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		var cr struct {
			Cart *struct {
				ID string `json:"id"`
			} `json:"cart"`
		}
		if err := json.Unmarshal(raw, &cr); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if cr.Cart == nil {
			t.Fatalf("no cart for session %s (body=%s)", session, raw)
		}
		return cr.Cart.ID
	}

	addItem := func(session string) string {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/cart", strings.NewReader(`{"variant_id":"`+variantOf(t, pool, tid, sfx)+`","quantity":1}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant-ID", tid)
		req.Header.Set("X-Customer-Session", session)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("add item: %v", err)
		}
		if res.StatusCode != fiber.StatusOK {
			raw, _ := io.ReadAll(res.Body)
			res.Body.Close()
			t.Fatalf("add item status %d: %s", res.StatusCode, raw)
		}
		res.Body.Close()
		return cartIDOf(session)
	}

	captureEmail := func(session, email string) {
		t.Helper()
		req := httptest.NewRequest("POST", "/api/v1/cart/email", strings.NewReader(`{"email":"`+email+`"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Tenant-ID", tid)
		req.Header.Set("X-Customer-Session", session)
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("capture email: %v", err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != fiber.StatusOK {
			t.Fatalf("capture email status %d: %s", res.StatusCode, raw)
		}
		var cr struct {
			Cart *struct {
				Email string `json:"email"`
			} `json:"cart"`
		}
		if err := json.Unmarshal(raw, &cr); err != nil {
			t.Fatalf("decode email cart: %v", err)
		}
		if cr.Cart == nil || cr.Cart.Email != email {
			t.Fatalf("cart.email should echo %s, got %+v", email, cr.Cart)
		}
	}

	backdate := func(session string) {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if _, err := tx.Exec(ctx,
			"UPDATE carts SET last_activity_at = now() - interval '2 hours' WHERE customer_session = $1", session); err != nil {
			t.Fatalf("backdate: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	recoverySet := func(session string) bool {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		var at *time.Time
		if err := tx.QueryRow(ctx,
			"SELECT recovery_sent_at FROM carts WHERE customer_session = $1", session).Scan(&at); err != nil {
			t.Fatalf("recovery_sent_at lookup: %v", err)
		}
		return at != nil
	}

	runSweep := func() (emailed []string) {
		t.Helper()
		var got []string
		n, err := SweepAbandonedCarts(ctx, pool, func(c context.Context, tenantID, cartID string) error {
			got = append(got, cartID)
			return nil
		})
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if n != len(got) {
			t.Fatalf("sweep reported %d but spy saw %d", n, len(got))
		}
		return got
	}

	// --- Candidate: idle >1h, has email + items ------------------------------
	abandonedSession := "sess-abandoned-" + sfx
	addItem(abandonedSession)
	captureEmail(abandonedSession, "recover@"+sfx+".example.com")
	// A fresh cart must NOT be swept yet.
	if got := runSweep(); len(got) != 0 {
		t.Fatalf("fresh cart should not be swept, got %v", got)
	}
	backdate(abandonedSession)
	abandonedCartID := cartIDOf(abandonedSession)

	got := runSweep()
	if len(got) != 1 || got[0] != abandonedCartID {
		t.Fatalf("sweep should email exactly the idle cart %s, got %v", abandonedCartID, got)
	}
	if !recoverySet(abandonedSession) {
		t.Fatal("recovery_sent_at should be stamped after first sweep")
	}
	// Idempotent: a second sweep must not email it again.
	if got := runSweep(); len(got) != 0 {
		t.Fatalf("recovered cart should not be swept again, got %v", got)
	}

	// --- Converted cart (checked out -> deleted) is never a candidate --------
	convertedSession := "sess-converted-" + sfx
	addItem(convertedSession)
	captureEmail(convertedSession, "converted@"+sfx+".example.com")
	backdate(convertedSession)
	// Checkout deletes the cart + items in the order tx (orders.go); simulate
	// the conversion by deleting the cart row, as checkout does.
	deleteCartOf(t, pool, tid, convertedSession)
	if got := runSweep(); len(got) != 0 {
		t.Fatalf("converted cart must never be swept, got %v", got)
	}

	// --- No email => never a candidate ---------------------------------------
	noEmailSession := "sess-noemail-" + sfx
	addItem(noEmailSession)
	backdate(noEmailSession)
	if got := runSweep(); len(got) != 0 {
		t.Fatalf("cart without email must not be swept, got %v", got)
	}

	// --- Email captured but no items => never a candidate --------------------
	emptySession := "sess-empty-" + sfx
	req := httptest.NewRequest("POST", "/api/v1/cart/email", strings.NewReader(`{"email":"empty@`+sfx+`.example.com"}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Tenant-ID", tid)
	req.Header.Set("X-Customer-Session", emptySession)
	if res, err := app.Test(req, -1); err != nil || res.StatusCode != fiber.StatusOK {
		t.Fatalf("capture email on empty cart: %v status %v", err, res.StatusCode)
	} else {
		res.Body.Close()
	}
	backdate(emptySession)
	if got := runSweep(); len(got) != 0 {
		t.Fatalf("item-less cart must not be swept, got %v", got)
	}

	// --- last_activity_at refreshes on cart mutations ------------------------
	activeSession := "sess-active-" + sfx
	addItem(activeSession)
	captureEmail(activeSession, "active@"+sfx+".example.com")
	backdate(activeSession)
	// Any mutation (PATCH quantity) refreshes activity => no longer a candidate.
	itID := itemIDOf(t, pool, tid, activeSession)
	patchReq := httptest.NewRequest("PATCH", "/api/v1/cart/items/"+itID, strings.NewReader(`{"quantity":2}`))
	patchReq.Header.Set("Content-Type", "application/json")
	patchReq.Header.Set("X-Tenant-ID", tid)
	patchReq.Header.Set("X-Customer-Session", activeSession)
	res, err := app.Test(patchReq, -1)
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	res.Body.Close()
	if got := runSweep(); len(got) != 0 {
		t.Fatalf("cart touched within the hour must not be swept, got %v", got)
	}

	// --- Email validation -----------------------------------------------------
	bad := httptest.NewRequest("POST", "/api/v1/cart/email", strings.NewReader(`{"email":"not-an-email"}`))
	bad.Header.Set("Content-Type", "application/json")
	bad.Header.Set("X-Tenant-ID", tid)
	bad.Header.Set("X-Customer-Session", activeSession)
	if res, err := app.Test(bad, -1); err != nil || res.StatusCode != fiber.StatusBadRequest {
		t.Fatalf("bad email should be 400, got %v %v", res.StatusCode, err)
	} else {
		res.Body.Close()
	}
}

func mkTenantID(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(context.Background(),
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		name, "recover-"+name+"-"+randSuffix4()).Scan(&id); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	return id
}

func variantOf(t *testing.T, pool *pgxpool.Pool, tid, sfx string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var pid, vid string
	if err := tx.QueryRow(ctx,
		"INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status) VALUES ($1, 'Recovery T', $2, 2000, 'usd', 99, 'active') RETURNING id",
		tid, "recovery-t-"+randSuffix4()).Scan(&pid); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if err := tx.QueryRow(ctx,
		"INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status) VALUES ($1, $2, 2000, 99, 'active') RETURNING id",
		tid, pid).Scan(&vid); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return vid
}

func deleteCartOf(t *testing.T, pool *pgxpool.Pool, tid, session string) {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM cart_items WHERE cart_id = (SELECT id FROM carts WHERE customer_session = $1)", session); err != nil {
		t.Fatalf("delete items: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"DELETE FROM carts WHERE customer_session = $1", session); err != nil {
		t.Fatalf("delete cart: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
}

func itemIDOf(t *testing.T, pool *pgxpool.Pool, tid, session string) string {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var id string
	if err := tx.QueryRow(ctx,
		"SELECT ci.id FROM cart_items ci JOIN carts c ON c.id = ci.cart_id WHERE c.customer_session = $1", session).Scan(&id); err != nil {
		t.Fatalf("item lookup: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}
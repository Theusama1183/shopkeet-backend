package notifications

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

func randSuffix4() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestSendCartAbandoned verifies the recovery sender: it renders + sends the
// cart and records a notification_log row (type cart_abandoned, order_id NULL)
// inside a tenant-scoped transaction. Once-only delivery is the sweep's job
// (recovery_sent_at stamp), so the sender itself may be invoked repeatedly and
// still log one row per invocation.
func TestSendCartAbandoned(t *testing.T) {
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
	tid, cartID, email := seedRecoveryCart(t, pool, sfx)

	svc := New(pool, nil, "shopkeet.com")
	svc.SendCartAbandoned(ctx, tid, cartID)

	count := logCount(t, pool, tid, cartID, email)
	if count != 1 {
		t.Fatalf("expected exactly 1 cart_abandoned log row for %s, got %d", email, count)
	}

	// A cart without a captured email sends nothing.
	_, emptyCartID, _ := seedRecoveryCart(t, pool, sfx)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"UPDATE carts SET customer_email = NULL WHERE id = $1", emptyCartID); err != nil {
		t.Fatalf("clear email: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	svc.SendCartAbandoned(ctx, tid, emptyCartID)
	if count := logCount(t, pool, tid, emptyCartID, ""); count != 0 {
		t.Fatalf("email-less cart must not be logged, got %d rows", count)
	}
}

func seedRecoveryCart(t *testing.T, pool *pgxpool.Pool, sfx string) (tid, cartID, email string) {
	t.Helper()
	ctx := context.Background()
	email = "recover@" + sfx + "example.com"

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"notif-"+sfx, "notif-"+randSuffix4()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var pid, vid string
	if err := tx.QueryRow(ctx,
		`INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		 VALUES ($1, 'Recovery T', $2, 2000, 'usd', 99, 'active') RETURNING id`,
		tid, "recovery-n-"+randSuffix4()).Scan(&pid); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if err := tx.QueryRow(ctx,
		`INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status)
		 VALUES ($1, $2, 2000, 99, 'active') RETURNING id`,
		tid, pid).Scan(&vid); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	if err := tx.QueryRow(ctx,
		"INSERT INTO carts (tenant_id, customer_session, customer_email) VALUES ($1, $2, $3) RETURNING id",
		tid, "sess-"+sfx, email).Scan(&cartID); err != nil {
		t.Fatalf("seed cart: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"INSERT INTO cart_items (tenant_id, cart_id, product_id, variant_id, quantity) VALUES ($1, $2, $3, $4, 2)",
		tid, cartID, pid, vid); err != nil {
		t.Fatalf("seed cart item: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return tid, cartID, email
}

func logCount(t *testing.T, pool *pgxpool.Pool, tid, cartID, email string) int {
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
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM notification_log
		WHERE notification_type = 'cart_abandoned'
		  AND recipient = $1
		  AND order_id IS NULL`, email).Scan(&n); err != nil {
		t.Fatalf("log count: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return n
}

// TestListLogRoute exercises GET /notifications/log end-to-end through the real
// middleware. It guards a regression where the handler type-asserted Locals("tx")
// against an inline interface instead of pgx.Tx; Go requires identical method
// signatures, so that assertion never matched and the endpoint answered 500 for
// every caller, on every tenant, in both an empty and a populated state.
func TestListLogRoute(t *testing.T) {
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

	const secret = "notif-log-test-secret"
	sfx := randSuffix4()
	tid, cartID, email := seedRecoveryCart(t, pool, sfx)

	// No rows yet: the route must still answer 200 with an empty list.
	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	RegisterRoutes(app.Group("/api/v1"), pool, secret)

	merchantID := seedMerchantUser(t, pool, tid, sfx)
	token, err := auth.Sign(secret, tid, merchantID, "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	call := func() (int, string) {
		t.Helper()
		req, _ := http.NewRequest("GET", "/api/v1/notifications/log", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := app.Test(req, 5000)
		if err != nil {
			t.Fatalf("app.Test: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	if code, body := call(); code != 200 {
		t.Fatalf("empty log: want 200, got %d (%s)", code, body)
	}

	// One row, written inside its own tenant-scoped tx the way the senders do.
	New(pool, nil, "shopkeet.com").SendCartAbandoned(ctx, tid, cartID)

	code, body := call()
	if code != 200 {
		t.Fatalf("populated log: want 200, got %d (%s)", code, body)
	}
	if !strings.Contains(body, email) {
		t.Fatalf("log body missing recipient %s: %s", email, body)
	}
	if strings.Contains(body, `"notifications":null`) {
		t.Fatalf("log serialised null instead of a list: %s", body)
	}
}

func seedMerchantUser(t *testing.T, pool *pgxpool.Pool, tid, sfx string) string {
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
	if err := tx.QueryRow(ctx, `
		INSERT INTO merchant_users (tenant_id, email, password_hash, role)
		VALUES ($1, $2, 'x', 'owner') RETURNING id`,
		tid, "notif-"+sfx+"@example.com").Scan(&id); err != nil {
		t.Fatalf("seed merchant user: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	return id
}

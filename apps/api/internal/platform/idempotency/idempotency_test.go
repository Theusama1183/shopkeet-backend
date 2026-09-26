package idempotency_test

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/idempotency"
)

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// helper to read a response body once.
func respBody(r *http.Response) string {
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

// TestIdempotencyReplay is the Phase 14 acceptance criterion: the same
// (tenant, endpoint, Idempotency-Key) after a successful call replays the
// stored response instead of running the handler again. A second, different
// key on the same endpoint still executes normally, and omitting the header
// passes through untouched.
func TestIdempotencyReplay(t *testing.T) {
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

	const secret = "test-secret"
	var tid string
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id`,
		"idem-alpha", "idem-alpha-"+randSuffix()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	call := 0
	app := fiber.New()
	v1 := app.Group("/api/v1")
	v1.Post("/idem",
		auth.TenantMW(pool, secret),
		idempotency.Middleware("POST /idem"),
		func(c *fiber.Ctx) error {
			call++
			return c.Status(fiber.StatusCreated).JSON(fiber.Map{"order": "created", "n": call})
		})

	do := func(key string) (*http.Response, error) {
		req := httptest.NewRequest("POST", "/api/v1/idem", bytes.NewBufferString(`{}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		if key != "" {
			req.Header.Set(idempotency.Header, key)
		}
		return app.Test(req, 10000)
	}

	// First call executes the handler; the response is cached.
	r1, err := do("k-1-abc")
	if err != nil {
		t.Fatalf("request 1: %v", err)
	}
	if w := r1.StatusCode; w != fiber.StatusCreated {
		t.Fatalf("first: want 201 got %d", w)
	}
	if call != 1 {
		t.Fatalf("handler executed %d times after first call", call)
	}

	// Retry with the same key replays: handler NOT re-run, and the stored body
	// is semantically the same JSON (JSONB re-canonicalizes spacing, so compare
	// decoded, not byte-for-byte).
	r2, err := do("k-1-abc")
	if err != nil {
		t.Fatalf("request 2: %v", err)
	}
	if w := r2.StatusCode; w != fiber.StatusCreated {
		t.Fatalf("replay: want 201 got %d", w)
	}
	if call != 1 {
		t.Fatalf("handler executed %d times after replay (want 1)", call)
	}
	var j1, j2 any
	if err := json.Unmarshal([]byte(respBody(r1)), &j1); err != nil {
		t.Fatalf("decode r1: %v", err)
	}
	if err := json.Unmarshal([]byte(respBody(r2)), &j2); err != nil {
		t.Fatalf("decode r2: %v", err)
	}
	if !reflect.DeepEqual(j1, j2) {
		t.Fatalf("replayed body differs: got %v want %v", j2, j1)
	}

	// A second, different key runs the handler afresh.
	r3, err := do("k-2-xyz")
	if err != nil {
		t.Fatalf("request 3: %v", err)
	}
	if call != 2 {
		t.Fatalf("handler executed %d times with new key (want 2)", call)
	}
	_ = r3

	// Omitting the header bypasses idempotency entirely.
	r4, err := do("")
	if err != nil {
		t.Fatalf("request 4: %v", err)
	}
	if w := r4.StatusCode; w != fiber.StatusCreated {
		t.Fatalf("no header: want 201 got %d", w)
	}
	if call != 3 {
		t.Fatalf("handler executed %d times without header (want 3)", call)
	}
}

// TestIdempotencyPurgeExpired verifies the maintenance job only removes rows
// older than the retention window and leaves fresh keys untouched.
func TestIdempotencyPurgeExpired(t *testing.T) {
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

	var tid string
	if err := pool.QueryRow(ctx,
		`INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id`,
		"idem-beta", "idem-beta-"+randSuffix()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	// Seed one key accepted 25h ago (expired) and one just now (fresh),
	// inside a tenant-scoped tx so RLS lets the write through.
	stx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin seed tx: %v", err)
	}
	defer stx.Rollback(ctx)
	if _, err := stx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := stx.Exec(ctx, `
		INSERT INTO idempotency_keys (tenant_id, endpoint, key, response_status, response_body, created_at)
		VALUES ($1, 'POST /checkout', 'expired-key', 201, '{}'::jsonb, now() - interval '25 hours'),
		       ($1, 'POST /checkout', 'fresh-key',  201, '{}'::jsonb, now())`,
		tid); err != nil {
		t.Fatalf("seed keys: %v", err)
	}
	if err := stx.Commit(ctx); err != nil {
		t.Fatalf("commit seed: %v", err)
	}

	n, err := idempotency.PurgeExpired(ctx, pool, time.Now().Add(-idempotency.Retention))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if n != 1 {
		t.Fatalf("purge removed %d rows (want 1)", n)
	}

	countKey := func(key string) int {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin count tx: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant for count: %v", err)
		}
		var n int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM idempotency_keys WHERE tenant_id = $1 AND key = $2`,
			tid, key).Scan(&n); err != nil {
			t.Fatalf("count %s: %v", key, err)
		}
		return n
	}

	if fresh := countKey("fresh-key"); fresh != 1 {
		t.Fatalf("fresh key lost after purge (want 1, got %d)", fresh)
	}
	if expired := countKey("expired-key"); expired != 0 {
		t.Fatalf("expired key survived purge (want 0, got %d)", expired)
	}
}
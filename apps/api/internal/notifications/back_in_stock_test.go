package notifications

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// countingProvider records every Notification it is asked to send.
type countingProvider struct {
	mu      sync.Mutex
	sent    int
	emails  []string
	types   []string
}

func (c *countingProvider) Name() string { return "count" }

func (c *countingProvider) Send(_ context.Context, n Notification) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sent++
	c.emails = append(c.emails, n.Recipient)
	c.types = append(c.types, n.Type)
	return nil
}

// TestDeliverBackInStockExactlyOnce is the Phase 19 acceptance criterion:
// restocking a variant notifies each waiting subscriber exactly once. Two
// subscribers => two sends + two back_in_stock log rows and both notified_at
// stamped; a second restock event (or a sub already notified) sends nothing.
func TestDeliverBackInStockExactlyOnce(t *testing.T) {
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

	sfx := randSuffix6()
	var tid, pid, vid string
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"bis-"+sfx, "bis-"+randSuffix6()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO products (tenant_id, name, slug, price_cents, currency, inventory_count, status)
		VALUES ($1, $2, $3, $4, 'usd', 0, 'active') RETURNING id`,
		tid, "BackSoon", "bis-"+sfx, 990).Scan(&pid); err != nil {
		t.Fatalf("seed product: %v", err)
	}
	if err := tx.QueryRow(ctx, `
		INSERT INTO product_variants (tenant_id, product_id, price_cents, inventory_count, status, allow_preorder)
		VALUES ($1, $2, $3, 0, 'active', false) RETURNING id`, tid, pid, 990).Scan(&vid); err != nil {
		t.Fatalf("seed variant: %v", err)
	}
	// One subscriber is already notified (edge case); the other two wait.
	if _, err := tx.Exec(ctx, `
		INSERT INTO back_in_stock_subscriptions (tenant_id, variant_id, email, notified_at)
		VALUES ($1, $2, 'already@example.com', now()),
		       ($1, $2, 'wait1@example.com', NULL),
		       ($1, $2, 'wait2@example.com', NULL)`, tid, vid); err != nil {
		t.Fatalf("seed subscriptions: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	prov := &countingProvider{}
	svc := New(pool, prov, "shopkeet.com")

	// First restock: exactly the two waiting subscribers, exactly once each.
	svc.deliverBackInStock(ctx, tid, vid, pid)
	time.Sleep(50 * time.Millisecond) // allow goroutine-free synchronous stretch
	if prov.sent != 2 {
		t.Fatalf("first restock sent %d, want 2", prov.sent)
	}
	got := map[string]bool{}
	for _, e := range prov.emails {
		got[e] = true
	}
	if !got["wait1@example.com"] || !got["wait2@example.com"] || got["already@example.com"] {
		t.Fatalf("restock recipients wrong: %v", prov.emails)
	}
	for _, typ := range prov.types {
		if typ != "back_in_stock" {
			t.Fatalf("unexpected notification type %q", typ)
		}
	}
	if count := backInStockLogCount(t, pool, tid); count != 2 {
		t.Fatalf("log rows = %d, want 2", count)
	}

	// Second restock event: nobody is re-notified (notified_at already set).
	svc.deliverBackInStock(ctx, tid, vid, pid)
	if prov.sent != 2 {
		t.Fatalf("second restock sent %d more, want exactly-once preserved", prov.sent)
	}
	if count := backInStockLogCount(t, pool, tid); count != 2 {
		t.Fatalf("log rows after second restock = %d, want 2", count)
	}

	// All three subscribers are stamped.
	tx, err = pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin read: %v", err)
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid)
	var unnotified int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM back_in_stock_subscriptions
		WHERE variant_id = $1 AND notified_at IS NULL`, vid).Scan(&unnotified); err != nil {
		t.Fatalf("read notified: %v", err)
	}
	if unnotified != 0 {
		t.Fatalf("%d subscriptions still waiting after restock", unnotified)
	}
	_ = tx.Commit(ctx)
}

func backInStockLogCount(t *testing.T, pool *pgxpool.Pool, tid string) int {
	t.Helper()
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	_, _ = tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid)
	var n int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM notification_log
		WHERE tenant_id = $1 AND notification_type = 'back_in_stock'`, tid).Scan(&n); err != nil {
		t.Fatalf("count log: %v", err)
	}
	_ = tx.Commit(ctx)
	return n
}

func randSuffix6() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
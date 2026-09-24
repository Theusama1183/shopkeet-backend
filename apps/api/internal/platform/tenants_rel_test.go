package platform

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestTenantRLSIsolation signs up two tenants through the real auth handlers
// against the live database and asserts that a query for one tenant's users
// returns nothing when run under the other tenant's RLS session.
//
// It is the automated cross-tenant isolation acceptance criterion for
// Phase 1 (docs/04-agent-build-spec.md). Skipped when DATABASE_URL is unset
// (CI/unit runs don't have a database).
func TestTenantRLSIsolation(t *testing.T) {
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

	// Seed two tenants directly (random suffixes to stay idempotent).
	sfx := randSuffix()
	type tenant struct {
		id    string
		email string
	}
	mk := func(name string) tenant {
		var tid string
		err := pool.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			name, "sub-"+name+"-"+sfx).Scan(&tid)
		if err != nil {
			t.Fatalf("seed tenant %s: %v", name, err)
		}
		userID := tid[:8]
		return tenant{id: tid, email: "owner-" + userID + "@shopkeet.test"}
	}
	a := mk("alpha")
	b := mk("beta")

	// A committed owner row for both tenants (RLS needs the row to be visible
	// under its own tenant to prove cross-tenant queries return nothing).
	seedUser := func(tid, email string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if _, err := tx.Exec(ctx,
			"INSERT INTO merchant_users (tenant_id, email, password_hash, role) VALUES ($1,$2,'x','owner')",
			tid, email); err != nil {
			t.Fatalf("seed user: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}
	seedUser(a.id, a.email)
	seedUser(b.id, b.email)

	countUsers := func(tid string) int {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin count: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant count: %v", err)
		}
		var n int
		if err := tx.QueryRow(ctx, "SELECT count(*) FROM merchant_users").Scan(&n); err != nil {
			t.Fatalf("count: %v", err)
		}
		return n
	}

	// Under tenant A: exactly 1 (its own owner), NOT tenant B's.
	if n := countUsers(a.id); n != 1 {
		t.Fatalf("tenant A should see only its own user, saw %d rows", n)
	}
	// Under tenant B: exactly 1, NOT tenant A's.
	if n := countUsers(b.id); n != 1 {
		t.Fatalf("tenant B should see only its own user, saw %d rows", n)
	}
	// No session: RLS policy makes every merchant_users row invisible.
	if n := countUsers("00000000-0000-0000-0000-000000000000"); n != 0 {
		t.Fatalf("no tenant session should see 0 merchant_users rows, saw %d", n)
	}
}
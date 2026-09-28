package giftcards

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/httperr"
)

// TestGiftCardsAdmin is the Phase 18 admin surface: issuing a card (with and
// without an explicit code), listing, and the input guards (zero amount,
// duplicate code). Issued cards are visible only to their tenant.
func TestGiftCardsAdmin(t *testing.T) {
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
	sfx := randSuffix()
	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"gctenant", "gc-"+sfx).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}
	token, err := auth.Sign(secret, tid, tid[:8], "owner", time.Hour)
	if err != nil {
		t.Fatalf("sign: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	RegisterRoutes(app.Group("/api/v1"), pool, secret, New(pool))

	do := func(method, path, token string, body string, want int) (*http.Response, string) {
		t.Helper()
		var req *http.Request
		if body == "" {
			req = httptest.NewRequest(method, path, nil)
		} else {
			req = httptest.NewRequest(method, path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
		}
		req.Header.Set("Authorization", "Bearer "+token)
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
		return res, string(raw)
	}

	// Zero amount rejected.
	do("POST", "/api/v1/gift-cards", token, `{"amount_cents":0}`, fiber.StatusBadRequest)

	// Issue with an explicit code.
	res, _ := do("POST", "/api/v1/gift-cards", token, `{"code":"GC-PROMO","amount_cents":2500}`,
		fiber.StatusCreated)
	var issued struct {
		ID        string `json:"id"`
		Code      string `json:"code"`
		Initial   int    `json:"initial_balance_cents"`
		Balance   int    `json:"balance_cents"`
		Status    string `json:"status"`
		ExpiresAt any    `json:"expires_at"`
	}
	if err := json.NewDecoder(res.Body).Decode(&issued); err != nil {
		t.Fatalf("decode issued: %v", err)
	}
	res.Body.Close()
	if issued.Code != "GC-PROMO" || issued.Initial != 2500 || issued.Balance != 2500 || issued.Status != "active" {
		t.Fatalf("unexpected issued card: %+v", issued)
	}

	// Duplicate code -> 409.
	do("POST", "/api/v1/gift-cards", token, `{"code":"GC-PROMO","amount_cents":100}`, fiber.StatusConflict)

	// Auto-generated code works.
	res, _ = do("POST", "/api/v1/gift-cards", token, `{"amount_cents":1000}`, fiber.StatusCreated)
	var gen struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(res.Body).Decode(&gen); err != nil {
		t.Fatalf("decode generated: %v", err)
	}
	res.Body.Close()
	if !strings.HasPrefix(gen.Code, "GC-") {
		t.Fatalf("expected generated code prefix GC-, got %q", gen.Code)
	}

	// List returns both with the newest first.
	res, body := do("GET", "/api/v1/gift-cards", token, "", fiber.StatusOK)
	var listed struct {
		Cards []struct {
			Code    string `json:"code"`
			Balance int    `json:"balance_cents"`
		} `json:"gift_cards"`
	}
	if err := json.NewDecoder(res.Body).Decode(&listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	res.Body.Close()
	if len(listed.Cards) != 2 {
		t.Fatalf("expected 2 cards, got %d (%s)", len(listed.Cards), body)
	}
	if listed.Cards[0].Code != gen.Code || listed.Cards[1].Code != "GC-PROMO" {
		t.Fatalf("expected newest-first ordering, got %+v", listed.Cards)
	}

	// No token -> 401 (middleware enforces tenant scope).
	do("GET", "/api/v1/gift-cards", "", "", fiber.StatusUnauthorized)
}

// TestResolveValidations covers the apply-time gates: only an active,
// unexpired card with remaining balance resolves; everything else returns a
// client-facing CodeError (the checkout and cart apply paths share this).
func TestResolveValidations(t *testing.T) {
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

	var tid string
	if err := pool.QueryRow(ctx,
		"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
		"gcval", "gcv-"+randSuffix()).Scan(&tid); err != nil {
		t.Fatalf("seed tenant: %v", err)
	}

	execAs := func(sql string, args ...any) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
			t.Fatalf("set tenant: %v", err)
		}
		if _, err := tx.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
	}

	// Valid card, disabled card, expired card, exhausted card.
	execAs(`INSERT INTO gift_cards (tenant_id, code, initial_balance_cents, balance_cents, status)
		VALUES ($1, 'GC-OK', 1000, 1000, 'active')`, tid)
	execAs(`INSERT INTO gift_cards (tenant_id, code, initial_balance_cents, balance_cents, status)
		VALUES ($1, 'GC-OFF', 1000, 1000, 'disabled')`, tid)
	execAs(`INSERT INTO gift_cards (tenant_id, code, initial_balance_cents, balance_cents, status, expires_at)
		VALUES ($1, 'GC-EXP', 1000, 1000, 'active', now() - interval '1 day')`, tid)
	execAs(`INSERT INTO gift_cards (tenant_id, code, initial_balance_cents, balance_cents, status)
		VALUES ($1, 'GC-ZERO', 500, 0, 'active')`, tid)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tid); err != nil {
		t.Fatalf("set tenant: %v", err)
	}

	q, err := Resolve(ctx, tx, tid, "GC-OK")
	if err != nil || q.Code != "GC-OK" || q.Cents != 1000 {
		t.Fatalf("GC-OK should resolve with 1000, got q=%+v err=%v", q, err)
	}

	for _, tc := range []struct {
		code    string
		status  int
		message string
	}{
		{"GC-OFF", fiber.StatusBadRequest, "not active"},
		{"GC-EXP", fiber.StatusBadRequest, "expired"},
		{"GC-ZERO", fiber.StatusBadRequest, "no remaining balance"},
		{"GC-MISSING", fiber.StatusNotFound, "not found"},
	} {
		_, err := Resolve(ctx, tx, tid, tc.code)
		var ce *CodeError
		if !errors.As(err, &ce) {
			t.Fatalf("%s: expected CodeError, got %v", tc.code, err)
		}
		if ce.Status != tc.status || !strings.Contains(ce.Message, tc.message) {
			t.Fatalf("%s: got status=%d msg=%q, want %d msg containing %q",
				tc.code, ce.Status, ce.Message, tc.status, tc.message)
		}
	}
}

// TestTenantScopeWithoutRLS is the regression test for the cross-tenant gift-card
// leak found in the Phase 18 live smoke: production's DATABASE_URL connects as a
// SUPERUSER role, which bypasses FORCE ROW LEVEL SECURITY, so a lookup that
// filtered only on `code` resolved another tenant's card (verified live — tenant
// B applied tenant A's GC-PARTIAL and got 200).
//
// RLS normally masks this class of bug, so the test deliberately connects as a
// superuser to reproduce the production condition. Two tenants, one card, and the
// non-owning tenant must not see or claim it.
func TestTenantScopeWithoutRLS(t *testing.T) {
	adminURL := rlsBypassURL(t)
	ctx := context.Background()

	// Seeding needs the app role (table owner) so the rows are created under the
	// tenant tables' normal ownership; the leak assertions then run as superuser.
	seed, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatalf("pgxpool (app role): %v", err)
	}
	defer seed.Close()

	sfx := randSuffix()
	var owner, other string
	for _, spec := range []struct {
		name string
		dst  *string
	}{{"gcowner-a", &owner}, {"gcowner-b", &other}} {
		if err := seed.QueryRow(ctx,
			"INSERT INTO tenants (name, subdomain) VALUES ($1, $2) RETURNING id",
			spec.name, spec.name+"-"+sfx).Scan(spec.dst); err != nil {
			t.Fatalf("seed tenant: %v", err)
		}
	}
	tx, err := seed.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", owner); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gift_cards (tenant_id, code, initial_balance_cents, balance_cents)
		VALUES ($1, 'GC-OWNED', 1000, 1000)`, owner); err != nil {
		t.Fatalf("seed card: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Now assert as the RLS-bypassing role that the non-owner cannot resolve or
	// claim the card, even with the tenant GUC pointing at the other tenant.
	admin, err := pgxpool.New(ctx, adminURL)
	if err != nil {
		t.Fatalf("pgxpool (superuser): %v", err)
	}
	defer admin.Close()

	run := func(tenantID string, fn func(tx pgx.Tx) error) error {
		rtx, err := admin.Begin(ctx)
		if err != nil {
			return err
		}
		defer rtx.Rollback(ctx)
		if _, err := rtx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
			return err
		}
		if err := fn(rtx); err != nil {
			return err
		}
		return rtx.Commit(ctx)
	}

	// Other tenant resolving the owner's code -> 404 CodeError.
	err = run(other, func(rtx pgx.Tx) error {
		q, err := Resolve(ctx, rtx, other, "GC-OWNED")
		var ce *CodeError
		if !errors.As(err, &ce) {
			t.Fatalf("other tenant resolved owner's card: q=%+v err=%v", q, err)
		}
		if ce.Status != fiber.StatusNotFound {
			t.Fatalf("expected 404 for another tenant's card, got %d (%s)", ce.Status, ce.Message)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("resolve cross-tenant: %v", err)
	}

	// Same for Claim, and the owner's balance must be untouched afterwards.
	err = run(other, func(rtx pgx.Tx) error {
		_, err := Claim(ctx, rtx, other, "GC-OWNED", 900)
		var ce *CodeError
		if !errors.As(err, &ce) {
			t.Fatalf("other tenant claimed owner's card: %v", err)
		}
		if ce.Status != fiber.StatusNotFound {
			t.Fatalf("expected 404 claiming another tenant's card, got %d", ce.Status)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("claim cross-tenant: %v", err)
	}

	var balance int
	balTx, err := seed.Begin(ctx)
	if err != nil {
		t.Fatalf("begin balance read: %v", err)
	}
	defer balTx.Rollback(ctx)
	if _, err := balTx.Exec(ctx,
		"SELECT set_config('app.current_tenant', $1, true)", owner); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	if err := balTx.QueryRow(ctx,
		"SELECT balance_cents FROM gift_cards WHERE tenant_id = $1 AND code = 'GC-OWNED'",
		owner).Scan(&balance); err != nil {
		t.Fatalf("balance check: %v", err)
	}
	if balance != 1000 {
		t.Fatalf("cross-tenant claim must not debit the card, balance = %d", balance)
	}

	// Sanity: the owning tenant still resolves and claims normally.
	if err := run(owner, func(rtx pgx.Tx) error {
		q, err := Resolve(ctx, rtx, owner, "GC-OWNED")
		if err != nil || q.Cents != 1000 {
			t.Fatalf("owner should resolve its own card: q=%+v err=%v", q, err)
		}
		return nil
	}); err != nil {
		t.Fatalf("owner resolve: %v", err)
	}
}

// rlsBypassURL derives a superuser connection string from DATABASE_URL so the
// cross-tenant tests reproduce production (where the API connects as a superuser
// and FORCE RLS is inert). The local/dev superuser is `shopkeet`.
func rlsBypassURL(t *testing.T) string {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL not set; skipping integration")
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		t.Fatalf("parse DATABASE_URL: %v", err)
	}
	if pw, ok := os.LookupEnv("SUPERUSER_DB_PASSWORD"); ok {
		u.User = url.UserPassword("shopkeet", pw)
	} else {
		u.User = url.UserPassword("shopkeet", "shopkeet")
	}
	bypass := u.String()

	// Only meaningful if the role actually bypasses RLS; otherwise the test would
	// pass for the wrong reason.
	pool, err := pgxpool.New(context.Background(), bypass)
	if err != nil {
		t.Skipf("no superuser connection available (%v); skipping RLS-bypass test", err)
	}
	defer pool.Close()
	var isSuper bool
	if err := pool.QueryRow(context.Background(),
		"SELECT usesuper FROM pg_user WHERE usename = current_user").Scan(&isSuper); err != nil {
		t.Skipf("cannot read role attributes: %v", err)
	}
	if !isSuper {
		t.Skip("connection role is not a superuser; RLS-bypass test would pass vacuously")
	}
	return bypass
}

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
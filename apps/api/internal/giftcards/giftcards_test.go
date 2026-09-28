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
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
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

	q, err := Resolve(ctx, tx, "GC-OK")
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
		_, err := Resolve(ctx, tx, tc.code)
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

func randSuffix() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
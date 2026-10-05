package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

func randSuffix6() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestLoginUnderRLS is the login acceptance criterion: signup works and the
// follow-up login must find the merchant across FORCE RLS. merchant_users is
// FORCE ROW LEVEL SECURITY with a tenant-scoped policy, and login knows no
// tenant at all now (the account is the identity), so credential verification and
// the store lookup both run through the RLS-free account-scoped tables from
// migration 0034. Regression guard for the 500/gone-credentials observed in
// production when login ran its lookup outside any RLS scope.
func TestLoginUnderRLS(t *testing.T) {
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
	sfx := randSuffix6()

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	// nil mailer: OTP disabled, signup/login hand tokens straight back — the
	// legacy shape this test asserts.
	RegisterRoutes(v1, pool, secret, ratelimit.New(nil), nil)

	type store struct {
		TenantID            string `json:"tenant_id"`
		Name                string `json:"name"`
		Subdomain           string `json:"subdomain"`
		Role                string `json:"role"`
		OnboardingCompleted bool   `json:"onboarding_completed"`
	}
	type loginResponse struct {
		Token               string  `json:"token"`
		OnboardingCompleted *bool   `json:"onboarding_completed"`
		Stores              []store `json:"stores"`
		StorePickToken      string  `json:"store_pick_token"`
	}

	post := func(path, body, bearer string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, raw
	}
	get := func(path, bearer string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest("GET", path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, raw
	}

	// signup takes only an account identity — no store name, no subdomain.
	emailA := "owner-a-" + sfx + "@example.com"
	code, raw := post("/api/v1/auth/signup",
		`{"email":"`+emailA+`","password":"hunter2hunter2"}`, "")
	if code != fiber.StatusCreated {
		t.Fatalf("signup -> %d (%s)", code, raw)
	}
	var signedUp struct {
		Token               string `json:"token"`
		OnboardingCompleted bool   `json:"onboarding_completed"`
		Store               store  `json:"store"`
	}
	if err := json.Unmarshal(raw, &signedUp); err != nil {
		t.Fatalf("decode signup: %v", err)
	}
	// Signup's session must be flagged as needing the wizard, or the admin guard
	// would drop a brand-new merchant straight into an unnamed dashboard.
	if signedUp.OnboardingCompleted {
		t.Fatalf("signup should hand out an un-onboarded session")
	}
	claims, err := Parse(secret, signedUp.Token)
	if err != nil {
		t.Fatalf("signup token does not parse: %v", err)
	}
	if claims.Scope != "merchant" || claims.Role != "owner" || claims.AccountID == "" {
		t.Fatalf("unexpected signup claims: %+v", claims)
	}
	if !NeedsOnboarding(claims) {
		t.Fatalf("signup token should require onboarding: %+v", claims)
	}
	if signedUp.Store.TenantID == "" || signedUp.Store.Subdomain == "" {
		t.Fatalf("signup must provision a store: %+v", signedUp.Store)
	}

	// One store: login returns a session directly.
	code, raw = post("/api/v1/auth/login",
		`{"email":"`+emailA+`","password":"hunter2hunter2"}`, "")
	if code != fiber.StatusOK {
		t.Fatalf("correct credentials -> %d (%s)", code, raw)
	}
	var one loginResponse
	if err := json.Unmarshal(raw, &one); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if one.Token == "" {
		t.Fatalf("single-store login must return a token: %s", raw)
	}
	if len(one.Stores) != 1 {
		t.Fatalf("expected exactly one store, got %d", len(one.Stores))
	}
	singleClaims, err := Parse(secret, one.Token)
	if err != nil {
		t.Fatalf("login token does not parse: %v", err)
	}
	if singleClaims.TenantID != one.Stores[0].TenantID || singleClaims.UserID == "" {
		t.Fatalf("login claims disagree with the store: %+v vs %+v", singleClaims, one.Stores[0])
	}

	if code, raw := post("/api/v1/auth/login",
		`{"email":"`+emailA+`","password":"wrong-password"}`, ""); code != fiber.StatusUnauthorized {
		t.Fatalf("wrong password -> %d (%s)", code, raw)
	}
	if code, raw := post("/api/v1/auth/login",
		`{"email":"ghost@`+sfx+`.com","password":"hunter2hunter2"}`, ""); code != fiber.StatusUnauthorized {
		t.Fatalf("unknown email -> %d (%s)", code, raw)
	}

	// --- multi-store ----------------------------------------------------------
	// A merchant on a paid plan can run several stores, so login must stop
	// guessing and ask. Grant account A a second store, the way a store-creation
	// path would.
	accountID := claims.AccountID
	second := "second-" + sfx
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)
	var tenantB string
	if err := tx.QueryRow(ctx,
		`INSERT INTO tenants (name, subdomain, onboarding_completed) VALUES ('Second', $1, true) RETURNING id`,
		second).Scan(&tenantB); err != nil {
		t.Fatalf("insert second tenant: %v", err)
	}
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_tenant', $1, true)", tenantB); err != nil {
		t.Fatalf("set tenant: %v", err)
	}
	var userB string
	if err := tx.QueryRow(ctx, `
		INSERT INTO merchant_users (tenant_id, account_id, email, password_hash, role)
		VALUES ($1, $2, $3, (SELECT password_hash FROM merchant_accounts WHERE id = $2), 'owner')
		RETURNING id`, tenantB, accountID, emailA).Scan(&userB); err != nil {
		t.Fatalf("insert second membership: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO merchant_account_stores (account_id, tenant_id, role) VALUES ($1, $2, 'owner')`,
		accountID, tenantB); err != nil {
		t.Fatalf("insert second mapping: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	// Several stores: no session, only a short-lived ticket plus the list.
	code, raw = post("/api/v1/auth/login",
		`{"email":"`+emailA+`","password":"hunter2hunter2"}`, "")
	if code != fiber.StatusOK {
		t.Fatalf("multi-store login -> %d (%s)", code, raw)
	}
	var many loginResponse
	if err := json.Unmarshal(raw, &many); err != nil {
		t.Fatalf("decode multi login: %v", err)
	}
	if many.Token != "" {
		t.Fatalf("multi-store login must not pick a store on the merchant's behalf: %s", raw)
	}
	if many.StorePickToken == "" || len(many.Stores) != 2 {
		t.Fatalf("multi-store login must return a ticket and every store: %s", raw)
	}

	// The list endpoint needs a token; it must not be readable anonymously.
	code, raw = get("/api/v1/auth/stores", "")
	if code != fiber.StatusUnauthorized {
		t.Fatalf("stores without a token -> %d (%s)", code, raw)
	}
	code, raw = get("/api/v1/auth/stores", many.StorePickToken)
	if code != fiber.StatusOK {
		t.Fatalf("stores with ticket -> %d (%s)", code, raw)
	}
	var listed struct {
		Stores []store `json:"stores"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("decode stores: %v", err)
	}
	if len(listed.Stores) != 2 {
		t.Fatalf("expected 2 stores, got %d", len(listed.Stores))
	}

	// Picking a store the account does not belong to must fail even with a valid
	// ticket — the ticket grants nothing on its own.
	var otherTenant string
	emailB := "owner-b-" + sfx + "@example.com"
	if code, raw := post("/api/v1/auth/signup",
		`{"email":"`+emailB+`","password":"hunter2hunter2"}`, ""); code != fiber.StatusCreated {
		t.Fatalf("second signup -> %d (%s)", code, raw)
	}
	if err := pool.QueryRow(ctx, `SELECT tenant_id FROM merchant_account_stores WHERE account_id <> $1
		LIMIT 1`, accountID).Scan(&otherTenant); err != nil {
		t.Fatalf("find foreign tenant: %v", err)
	}
	if code, raw := post("/api/v1/auth/select-store",
		`{"tenant_id":"`+otherTenant+`"}`, many.StorePickToken); code != fiber.StatusForbidden {
		t.Fatalf("select foreign store -> %d (%s)", code, raw)
	}

	// Picking its own store yields a working tenant session.
	code, raw = post("/api/v1/auth/select-store",
		`{"tenant_id":"`+tenantB+`"}`, many.StorePickToken)
	if code != fiber.StatusOK {
		t.Fatalf("select own store -> %d (%s)", code, raw)
	}
	var selected struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(raw, &selected); err != nil {
		t.Fatalf("decode select-store: %v", err)
	}
	picked, err := Parse(secret, selected.Token)
	if err != nil {
		t.Fatalf("selected token does not parse: %v", err)
	}
	if picked.TenantID != tenantB || picked.UserID != userB || picked.Scope != "merchant" {
		t.Fatalf("select-store claims wrong: %+v", picked)
	}
	// That second store already finished onboarding, so it must not be sent back
	// through the wizard.
	if NeedsOnboarding(picked) {
		t.Fatalf("onboarded store flagged for onboarding: %+v", picked)
	}

	// An already-signed-in merchant can switch stores without logging in again.
	code, raw = post("/api/v1/auth/select-store",
		`{"tenant_id":"`+signedUp.Store.TenantID+`"}`, one.Token)
	if code != fiber.StatusOK {
		t.Fatalf("switch store with merchant token -> %d (%s)", code, raw)
	}

	// A duplicate account must be refused, and the first store must survive it.
	if code, raw := post("/api/v1/auth/signup",
		`{"email":"`+strings.ToUpper(emailA)+`","password":"hunter2hunter2"}`, ""); code != fiber.StatusConflict {
		t.Fatalf("duplicate account (different case) -> %d (%s)", code, raw)
	}
}

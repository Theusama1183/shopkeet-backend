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
)

func randSuffix6() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// TestLoginUnderRLS is the login acceptance criterion: signup works and the
// follow-up login must locate the merchant across FORCE RLS. merchant_users is
// FORCE ROW LEVEL SECURITY with a tenant-scoped policy, but at login time the
// caller only knows subdomain+email — so LoginHandler resolves the tenant first
// (tenants is intentionally RLS-free) and verifies credentials inside a
// transaction pinned to that tenant. Regression guard for the 500/gone-credentials
// observed in production when login ran its lookup outside any RLS scope.
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
	RegisterRoutes(v1, pool, secret)

	// do signs up a fresh tenant and returns its subdomain + owner email.
	do := func(name, subdomain string) (string, string) {
		t.Helper()
		email := "owner@" + name + "-" + sfx + ".com"
		body := `{"name":"` + name + `","subdomain":"` + subdomain + `","email":"` +
			email + `","password":"hunter2hunter2"}`
		req := httptest.NewRequest("POST", "/api/v1/auth/signup", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("signup %s: %v", name, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != fiber.StatusCreated {
			t.Fatalf("signup %s: status %d (%s)", name, res.StatusCode, raw)
		}
		return subdomain, email
	}

	// login posts subdomain+email+password and returns status + response body.
	login := func(subdomain, email, password string) (int, string) {
		t.Helper()
		body := `{"subdomain":"` + subdomain + `","email":"` + email +
			`","password":"` + password + `"}`
		req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("login: %v", err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(raw)
	}

	subA, emailA := do("A", "auth-A-"+sfx)
	subB, _ := do("B", "auth-B-"+sfx)

	if code, raw := login(subA, emailA, "hunter2hunter2"); code != fiber.StatusOK {
		t.Fatalf("correct credentials -> %d (%s)", code, raw)
	}
	var out struct {
		Token string `json:"token"`
	}
	body := `{"subdomain":"` + subA + `","email":"` + emailA + `","password":"hunter2hunter2"}`
	req := httptest.NewRequest("POST", "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	res, err := app.Test(req, -1)
	if err != nil {
		t.Fatalf("login for token: %v", err)
	}
	raw, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	claims, err := Parse(secret, out.Token)
	if err != nil {
		t.Fatalf("issued token does not parse: %v", err)
	}
	if claims.TenantID == "" || claims.Role != "owner" {
		t.Fatalf("unexpected claims: %+v", claims)
	}

	if code, raw := login(subA, emailA, "wrong-password"); code != fiber.StatusUnauthorized {
		t.Fatalf("wrong password -> %d (%s)", code, raw)
	}
	if code, raw := login("no-such-"+sfx, emailA, "hunter2hunter2"); code != fiber.StatusUnauthorized {
		t.Fatalf("unknown subdomain -> %d (%s)", code, raw)
	}
	if code, raw := login(subA, "ghost@"+sfx+".com", "hunter2hunter2"); code != fiber.StatusUnauthorized {
		t.Fatalf("unknown email -> %d (%s)", code, raw)
	}
	if code, raw := login(subB, emailA, "hunter2hunter2"); code != fiber.StatusUnauthorized {
		t.Fatalf("cross-tenant (B subdomain + A email) -> %d (%s)", code, raw)
	}
	if code, raw := login("", emailA, "hunter2hunter2"); code != fiber.StatusUnauthorized {
		t.Fatalf("empty subdomain -> %d (%s)", code, raw)
	}
}
package auth

import (
	"context"
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

// fakeMail is an in-memory Mailer: no network, no provider — the test reads
// the code straight out of it, exactly like an inbox.
type fakeMail struct {
	lastEmail string
	lastCode  string
	lastToken string
}

func (f *fakeMail) SendOTP(_ context.Context, email, code string) error {
	f.lastEmail, f.lastCode = email, code
	return nil
}

func (f *fakeMail) SendPasswordReset(_ context.Context, email, token string) error {
	f.lastEmail, f.lastToken = email, token
	return nil
}

// TestVerificationFlow is the acceptance test for the OTP gate: signup and
// login stop at {mode:"otp_required"} with NO token, the emailed code is the
// only thing that produces a session, resends invalidate predecessors, the
// guess budget actually locks, and forgot/reset round-trips a new password.
// Requires DATABASE_URL (skips locally, runs in CI/prod-backed runs).
func TestVerificationFlow(t *testing.T) {
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
	mail := &fakeMail{}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, secret, ratelimit.New(nil), mail)

	post := func(path, body string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, raw
	}

	type otpPhase struct {
		Mode   string `json:"mode"`
		Email  string `json:"email"`
		Method string `json:"method"`
		Token  string `json:"token"`
	}
	type session struct {
		Token      string `json:"token"`
		StorePick  string `json:"store_pick_token"`
		Onboarding bool   `json:"onboarding_completed"`
	}

	email := "otp-" + sfx + "@example.com"
	const pass = "hunter2hunter2"

	// --- signup stops at the OTP step, no token -----------------------------
	code, raw := post("/api/v1/auth/signup", `{"email":"`+email+`","password":"`+pass+`"}`)
	if code != fiber.StatusCreated {
		t.Fatalf("signup -> %d (%s)", code, raw)
	}
	var signup otpPhase
	if err := json.Unmarshal(raw, &signup); err != nil {
		t.Fatalf("decode signup: %v", err)
	}
	if signup.Mode != "otp_required" || signup.Token != "" ||
		signup.Email != email || signup.Method != "email" {
		t.Fatalf("signup must return otp_required and no token: %s", raw)
	}
	if mail.lastCode == "" {
		t.Fatalf("signup must mail a code")
	}

	// --- login with the right password is still only half the gate ----------
	code, raw = post("/api/v1/auth/login", `{"email":"`+email+`","password":"`+pass+`"}`)
	if code != fiber.StatusOK {
		t.Fatalf("login -> %d (%s)", code, raw)
	}
	var login otpPhase
	if err := json.Unmarshal(raw, &login); err != nil {
		t.Fatalf("decode login: %v", err)
	}
	if login.Mode != "otp_required" || login.Token != "" {
		t.Fatalf("login must gate on otp: %s", raw)
	}
	// The login round re-issued the code; the signup round's code is dead.
	// Verify must accept only what the inbox holds now.
	firstCode := mail.lastCode

	// --- resend invalidates the predecessor ---------------------------------
	if code, raw := post("/api/v1/auth/otp/send",
		`{"email":"`+email+`","contactMethod":"email"}`); code != fiber.StatusOK {
		t.Fatalf("otp resend -> %d (%s)", code, raw)
	}
	freshCode := mail.lastCode
	if freshCode == firstCode {
		t.Fatalf("resend must rotate the code")
	}
	if code, raw := post("/api/v1/auth/otp/verify",
		`{"email":"`+email+`","code":"`+firstCode+`"}`); code != fiber.StatusBadRequest {
		t.Fatalf("stale code -> %d (%s)", code, raw)
	}

	// Wrong email and empty/garbage codes never mint a session.
	if code, raw := post("/api/v1/auth/otp/verify",
		`{"email":"ghost-`+sfx+`@example.com","code":"123456"}`); code != fiber.StatusBadRequest {
		t.Fatalf("unknown email verify -> %d (%s)", code, raw)
	}
	if code, raw := post("/api/v1/auth/otp/verify",
		`{"email":"`+email+`","code":"000000"}`); code != fiber.StatusBadRequest {
		t.Fatalf("wrong code -> %d (%s)", code, raw)
	}

	// SMS contact is refused loudly; resend without a live code looks expired
	// (anti-enumeration: same error whether the row never existed or timed out).
	if code, raw := post("/api/v1/auth/otp/send",
		`{"email":"`+email+`","contactMethod":"sms"}`); code != fiber.StatusBadRequest {
		t.Fatalf("sms contact -> %d (%s)", code, raw)
	}
	if code, raw := post("/api/v1/auth/otp/send",
		`{"email":"nobody-`+sfx+`@example.com","contactMethod":"email"}`); code != fiber.StatusBadRequest {
		t.Fatalf("resend with no live code -> %d (%s)", code, raw)
	}

	// --- the right code produces the session signup withheld ----------------
	code, raw = post("/api/v1/auth/otp/verify",
		`{"email":"`+email+`","code":"`+freshCode+`"}`)
	if code != fiber.StatusOK {
		t.Fatalf("correct code -> %d (%s)", code, raw)
	}
	var verified session
	if err := json.Unmarshal(raw, &verified); err != nil {
		t.Fatalf("decode verify: %v", err)
	}
	if verified.Token == "" || verified.Onboarding {
		t.Fatalf("verify must hand back an un-onboarded session: %s", raw)
	}
	if claims, err := Parse(secret, verified.Token); err != nil ||
		claims.Scope != "merchant" || claims.AccountID == "" {
		t.Fatalf("verified token bad: %+v (%v)", claims, err)
	}

	// --- five wrong guesses burn the code; resend then looks expired ---------
	// The success above consumed the row — re-arm first, exactly like a user
	// who comes back for another round.
	code, raw = post("/api/v1/auth/login", `{"email":"`+email+`","password":"`+pass+`"}`)
	if code != fiber.StatusOK || mail.lastCode == "" {
		t.Fatalf("re-arm before burn -> %d (%s)", code, raw)
	}
	burned := false
	for i := 0; i < otpMaxAttempts+2; i++ {
		code, raw = post("/api/v1/auth/otp/verify",
			`{"email":"`+email+`","code":"999999"}`)
		if strings.Contains(string(raw), "request a new code") {
			burned = true
			break
		}
	}
	if !burned {
		t.Fatalf("wrong guesses must burn the code with a retry hint: %d (%s)", code, raw)
	}
	if code, raw := post("/api/v1/auth/otp/send",
		`{"email":"`+email+`","contactMethod":"email"}`); code != fiber.StatusBadRequest {
		t.Fatalf("resend after burn -> %d (%s)", code, raw)
	}

	// A fresh login re-arms the gate (new code, resend window restarted).
	code, raw = post("/api/v1/auth/login", `{"email":"`+email+`","password":"`+pass+`"}`)
	if code != fiber.StatusOK {
		t.Fatalf("login after burn -> %d (%s)", code, raw)
	}
	if mail.lastCode == "" {
		t.Fatalf("login must re-issue a code")
	}

	// --- forgot / reset round trip ------------------------------------------
	// Unknown address answers identically — the endpoint is not an oracle.
	if code, raw := post("/api/v1/auth/forgot-password",
		`{"email":"ghost-`+sfx+`@example.com"}`); code != fiber.StatusOK ||
		!strings.Contains(string(raw), `"sent":true`) {
		t.Fatalf("forgot unknown -> %d (%s)", code, raw)
	}
	if mail.lastToken != "" {
		t.Fatalf("unknown address must not mint a reset token")
	}
	if code, raw := post("/api/v1/auth/forgot-password",
		`{"email":"`+email+`"}`); code != fiber.StatusOK {
		t.Fatalf("forgot known -> %d (%s)", code, raw)
	}
	resetToken := mail.lastToken
	if resetToken == "" {
		t.Fatalf("forgot must mail a reset token")
	}

	if code, raw := post("/api/v1/auth/reset-password",
		`{"token":"`+resetToken+`-bogus","password":"brandnewpass1"}`); code != fiber.StatusBadRequest {
		t.Fatalf("bogus reset token -> %d (%s)", code, raw)
	}
	if code, raw := post("/api/v1/auth/reset-password",
		`{"token":"`+resetToken+`","password":"short"}`); code != fiber.StatusBadRequest {
		t.Fatalf("weak password -> %d (%s)", code, raw)
	}

	code, raw = post("/api/v1/auth/reset-password",
		`{"token":"`+resetToken+`","password":"brandnewpass1"}`)
	if code != fiber.StatusOK {
		t.Fatalf("reset -> %d (%s)", code, raw)
	}
	var reset session
	if err := json.Unmarshal(raw, &reset); err != nil {
		t.Fatalf("decode reset: %v", err)
	}
	if reset.Token == "" {
		t.Fatalf("reset must sign the merchant straight in: %s", raw)
	}
	// The consumed link cannot be replayed.
	if code, raw := post("/api/v1/auth/reset-password",
		`{"token":"`+resetToken+`","password":"anotherpass1"}`); code != fiber.StatusBadRequest {
		t.Fatalf("reset link reuse -> %d (%s)", code, raw)
	}

	// Old password is dead; the new one gets through the credential gate.
	if code, raw := post("/api/v1/auth/login",
		`{"email":"`+email+`","password":"`+pass+`"}`); code != fiber.StatusUnauthorized {
		t.Fatalf("old password after reset -> %d (%s)", code, raw)
	}
	code, raw = post("/api/v1/auth/login",
		`{"email":"`+email+`","password":"brandnewpass1"}`)
	if code != fiber.StatusOK || !strings.Contains(string(raw), "otp_required") {
		t.Fatalf("new password -> %d (%s)", code, raw)
	}
	if code, raw := post("/api/v1/auth/otp/verify",
		`{"email":"`+email+`","code":"`+mail.lastCode+`"}`); code != fiber.StatusOK {
		t.Fatalf("verify after reset -> %d (%s)", code, raw)
	}

	// --- the hourly guess budget locks regardless of reissues ---------------
	// Wrong guesses accumulate ACROSS codes (that is the point of the sum), so
	// burning the budget forces a 429 even though every individual row died
	// after otpMaxAttempts tries.
	for round := 0; round < 10; round++ {
		locked := false
		for i := 0; i < otpMaxAttempts; i++ {
			code, raw = post("/api/v1/auth/otp/verify",
				`{"email":"`+email+`","code":"999999"}`)
			if code == fiber.StatusTooManyRequests {
				locked = true
				break
			}
		}
		if locked {
			break
		}
		// Re-arm: password check restarts the code chain, not the guess sum.
		if code, raw = post("/api/v1/auth/login",
			`{"email":"`+email+`","password":"brandnewpass1"}`); code != fiber.StatusOK {
			t.Fatalf("re-arm login -> %d (%s)", code, raw)
		}
	}
	if code != fiber.StatusTooManyRequests {
		t.Fatalf("guess cap must eventually lock with 429: last %d (%s)", code, raw)
	}
}

// TestTrustedDeviceBypass is the acceptance test for "verify once": a browser
// that verified a code becomes trusted, so later logins with the same password
// skip the OTP step entirely — while an untrusted browser still gets the full
// gate, garbage tickets are ignored, and changing the password revokes every
// outstanding ticket (the ticket is signed under the account's password hash).
func TestTrustedDeviceBypass(t *testing.T) {
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
	mail := &fakeMail{}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	v1 := app.Group("/api/v1")
	RegisterRoutes(v1, pool, secret, ratelimit.New(nil), mail)

	post := func(path, body string, headers map[string]string) (int, []byte) {
		t.Helper()
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := app.Test(req, -1)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		raw, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, raw
	}

	type otpPhase struct {
		Mode   string `json:"mode"`
		Token  string `json:"token"`
	}
	type verifyResp struct {
		Token       string `json:"token"`
		DeviceToken string `json:"device_token"`
	}

	email := "bypass-" + sfx + "@example.com"
	const pass = "hunter2hunter2"
	verifiedAlso := func(raw []byte, label string) {
		t.Helper()
		var out verifyResp
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("%s: decode %v", label, err)
		}
		if out.Token == "" || out.DeviceToken == "" {
			t.Fatalf("%s: expected session + device_token: %s", label, raw)
		}
	}

	// --- signup gates on OTP, verify mints the bypass ticket ---------------
	if code, raw := post("/api/v1/auth/signup",
		`{"email":"`+email+`","password":"`+pass+`"}`, nil); code != fiber.StatusCreated {
		t.Fatalf("signup -> %d (%s)", code, raw)
	}
	code, raw := post("/api/v1/auth/otp/verify",
		`{"email":"`+email+`","code":"`+mail.lastCode+`"}`, nil)
	if code != fiber.StatusOK {
		t.Fatalf("verify -> %d (%s)", code, raw)
	}
	verifiedAlso(raw, "first verify")

	// --- trusted login skips the code and does NOT mail a fresh one ---------
	code, raw = post("/api/v1/auth/login",
		`{"email":"`+email+`","password":"`+pass+`"}`, nil)
	if code != fiber.StatusOK {
		t.Fatalf("control login -> %d (%s)", code, raw)
	}
	var control otpPhase
	if err := json.Unmarshal(raw, &control); err != nil {
		t.Fatalf("control decode: %v", err)
	}
	if control.Mode != "otp_required" || control.Token != "" {
		t.Fatalf("untrusted login must still gate on otp: %s", raw)
	}

	code, raw = post("/api/v1/auth/login",
		`{"email":"`+email+`","password":"`+pass+`"}`,
		map[string]string{"X-Otp-Bypass": "totally-forged"})
	if code != fiber.StatusOK {
		t.Fatalf("forged bypass login -> %d (%s)", code, raw)
	}
	var forged otpPhase
	if err := json.Unmarshal(raw, &forged); err != nil {
		t.Fatalf("forged decode: %v", err)
	}
	if forged.Mode != "otp_required" || forged.Token != "" {
		t.Fatalf("forged bypass must fall through to otp: %s", raw)
	}

	// Mint a REAL bypass ticket to answer the server's own gate.
	code, raw = post("/api/v1/auth/otp/verify",
		`{"email":"`+email+`","code":"`+mail.lastCode+`"}`, nil)
	if code != fiber.StatusOK {
		t.Fatalf("re-verify for ticket -> %d (%s)", code, raw)
	}
	var withTicket verifyResp
	if err := json.Unmarshal(raw, &withTicket); err != nil {
		t.Fatalf("ticket decode: %v", err)
	}
	if withTicket.DeviceToken == "" {
		t.Fatalf("verify must hand back a device_token: %s", raw)
	}
	before := mail.lastCode

	hosted := map[string]string{"X-Otp-Bypass": withTicket.DeviceToken}
	code, raw = post("/api/v1/auth/login",
		`{"email":"`+email+`","password":"`+pass+`"}`, hosted)
	if code != fiber.StatusOK {
		t.Fatalf("trusted login -> %d (%s)", code, raw)
	}
	var trusted verifyResp
	if err := json.Unmarshal(raw, &trusted); err != nil {
		t.Fatalf("trusted login decode: %v", err)
	}
	// The point of the ticket is the session, straight from the password. A
	// login never re-mints a device_token — the browser keeps the one it has.
	if trusted.Token == "" {
		t.Fatalf("trusted login must hand back a session: %s", raw)
	}
	if claims, err := Parse(secret, trusted.Token); err != nil ||
		claims.Scope != "merchant" || claims.AccountID == "" {
		t.Fatalf("trusted token bad: %+v (%v)", claims, err)
	}
	if mail.lastCode != before {
		t.Fatalf("trusted login must not issue a fresh code")
	}

	// --- password change revokes the outstanding ticket --------------------
	if code, raw := post("/api/v1/auth/forgot-password",
		`{"email":"`+email+`"}`, nil); code != fiber.StatusOK {
		t.Fatalf("forgot -> %d (%s)", code, raw)
	}
	resetToken := mail.lastToken
	if resetToken == "" {
		t.Fatalf("forgot must mail a reset token")
	}
	if code, raw := post("/api/v1/auth/reset-password",
		`{"token":"`+resetToken+`","password":"brandnewpass1"}`, nil); code != fiber.StatusOK {
		t.Fatalf("reset -> %d (%s)", code, raw)
	}
	code, raw = post("/api/v1/auth/login",
		`{"email":"`+email+`","password":"brandnewpass1"}`, hosted)
	if code != fiber.StatusOK {
		t.Fatalf("login after reset -> %d (%s)", code, raw)
	}
	var revoked otpPhase
	if err := json.Unmarshal(raw, &revoked); err != nil {
		t.Fatalf("revoked decode: %v", err)
	}
	if revoked.Mode != "otp_required" || revoked.Token != "" {
		t.Fatalf("bypass must not survive a password change: %s", raw)
	}
}

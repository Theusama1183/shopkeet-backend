package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"log"
	"math/big"
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Mailer delivers the two account-level emails this package triggers: the
// signup/login OTP and the password-reset link. It is defined HERE, not in
// notifications — notifications' tests import auth, so auth importing
// notifications would close the import loop. main wires the adapter
// (notifications.AuthMailer) into RegisterRoutes.
//
// A nil Mailer disables every OTP/reset code path (signup/login fall back to
// issuing sessions directly). main passes nil when no real mail provider is
// configured, so a dev/prod box without SMTP/Resend can never lock its own
// users out of their accounts behind codes nobody can read.
type Mailer interface {
	SendOTP(ctx context.Context, email, code string) error
	SendPasswordReset(ctx context.Context, email, token string) error
}

// Purpose strings for auth_verification_codes (CHECK-constrained in the table).
const (
	purposeOTP           = "otp"
	purposePasswordReset = "password_reset"
)

// OTP/reset economics. otpResendWindow bounds the total lifetime of one login
// session's code chain (a resend needs a live row created inside it, and
// issueCode restarts it only after a fresh password check); otpMaxAttempts is
// guesses against one row; otpHourlyCap is the cross-code guess budget per
// email per hour — the thing an IP-distributed guesser can't route around,
// because it is a plain sum in the DB with no IP in sight.
const (
	otpTTL           = 10 * time.Minute // matches the email copy
	otpResendWindow  = 30 * time.Minute
	otpMaxAttempts   = 5
	otpHourlyCap     = 20
	passwordResetTTL = time.Hour // matches the email copy
)

// errVerificationUnavailable is returned by every endpoint that needs a mailer
// when none is configured: fail closed rather than pretend to send.
var errVerificationUnavailable = httperr.New(
	fiber.StatusServiceUnavailable, "verification_unavailable",
	"Account verification is not available right now.")

// --- primitives ----------------------------------------------------------------

// hashSecret is what the DB stores for a code or reset token. The plaintext
// never lands in a row, so a DB leak hands an attacker nothing that verifies.
func hashSecret(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// secretMatches compares a candidate against a stored sha256 in constant time.
func secretMatches(storedHash, candidate string) bool {
	return subtle.ConstantTimeCompare(
		[]byte(storedHash), []byte(hashSecret(candidate))) == 1
}

// randomCode returns a uniformly random 6-digit code (zero-padded).
func randomCode() (string, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(1_000_000))
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%06d", n.Int64()), nil
}

// randomToken returns a 256-bit URL-safe token for the password-reset link.
func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// pruneCodes deletes an email's day-old code rows. Best-effort: a cleanup
// failure must never fail the login sitting in front of it. Rows younger than
// a day are kept even when consumed — the hourly guess-sum reads them.
func pruneCodes(ctx context.Context, pool *pgxpool.Pool, email string) {
	_, _ = pool.Exec(ctx, `
		DELETE FROM auth_verification_codes
		WHERE email = $1 AND created_at < now() - interval '1 day'`, email)
}

// issueCode writes a fresh OTP for an account that just passed a password
// check, consuming any live predecessor so exactly one code can ever verify.
// created_at restarts the resend window: the password gate re-armed it.
func issueCode(ctx context.Context, pool *pgxpool.Pool, accountID, email string, ttl time.Duration) (string, error) {
	code, err := randomCode()
	if err != nil {
		return "", err
	}
	pruneCodes(ctx, pool, email)
	if _, err := pool.Exec(ctx, `
		UPDATE auth_verification_codes SET consumed_at = now()
		WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL`,
		email, purposeOTP); err != nil {
		return "", err
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO auth_verification_codes (account_id, email, purpose, code_hash, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))`,
		accountID, email, purposeOTP, hashSecret(code), ttl.Seconds()); err != nil {
		return "", err
	}
	return code, nil
}

// liveCodeRow is the unexpired, unconsumed OTP row for an email, newest first.
type liveCodeRow struct {
	ID        string
	AccountID string
	Attempts  int
	CreatedAt time.Time
	ExpiresAt time.Time
}

func fetchLiveOTP(ctx context.Context, pool *pgxpool.Pool, email string) (liveCodeRow, bool, error) {
	var r liveCodeRow
	err := pool.QueryRow(ctx, `
		SELECT id, account_id, attempts, created_at, expires_at
		FROM auth_verification_codes
		WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL AND expires_at > now()
		ORDER BY created_at DESC LIMIT 1`,
		email, purposeOTP).Scan(&r.ID, &r.AccountID, &r.Attempts, &r.CreatedAt, &r.ExpiresAt)
	if err == pgx.ErrNoRows {
		return r, false, nil
	}
	if err != nil {
		return r, false, err
	}
	return r, true, nil
}

// hourlyGuesses is the cross-code brute-force budget: every failed attempt an
// email's address has burned in the last hour, across reissues. Checked before
// any code comparison so the 21st guess never reaches a hash.
func hourlyGuesses(ctx context.Context, pool *pgxpool.Pool, email string) (int, error) {
	var n int
	err := pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(attempts), 0)
		FROM auth_verification_codes
		WHERE email = $1 AND purpose = $2 AND created_at > now() - interval '1 hour'`,
		email, purposeOTP).Scan(&n)
	return n, err
}

// --- handlers ------------------------------------------------------------------

type otpRequest struct {
	Email         string `json:"email"`
	ContactMethod string `json:"contactMethod"`
}

// OTPSendHandler re-issues the code for a login/signup already in flight. It
// fails closed: without a live code there is nothing to resend, and answering
// "sent" anyway would both lie to the user and turn an authenticated-ish
// endpoint into an unauthenticated code-request path. The same
// verification_expired error covers a missing row (anti-enumeration).
func OTPSendHandler(pool *pgxpool.Pool, mailer Mailer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if mailer == nil {
			return errVerificationUnavailable
		}
		var req otpRequest
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		if req.Email == "" {
			return httperr.C(fiber.StatusBadRequest, "email required")
		}
		// The page only ever asks for email, but an SMS request must not be
		// answered as if it were an email one.
		if m := strings.ToLower(strings.TrimSpace(req.ContactMethod)); m != "" && m != "email" {
			return httperr.New(fiber.StatusBadRequest, "sms_unavailable", "SMS codes are not available yet - we only send by email.")
		}

		ctx := c.Context()
		row, ok, err := fetchLiveOTP(ctx, pool, req.Email)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		// Resend is only allowed while the ORIGINAL issue is fresh: the resend
		// window is anchored at created_at, which a resend deliberately does
		// not touch (that is what keeps the window bounded).
		if !ok ||
			time.Since(row.CreatedAt) > otpResendWindow ||
			time.Until(row.ExpiresAt) <= 0 {
			return httperr.New(fiber.StatusBadRequest, "verification_expired",
				"That code has expired — log in again to get a new one.")
		}

		code, err := randomCode()
		if err != nil {
			return httperr.ErrInternalServerError
		}
		// Refresh in place: same row (created_at anchors the window), new
		// code, new expiry, attempts carried over — guesses do not reset, or
		// resending would launder the hourly cap.
		if _, err := pool.Exec(ctx, `
			UPDATE auth_verification_codes
			SET code_hash = $2, expires_at = now() + make_interval(secs => $3)
			WHERE id = $1`,
			row.ID, hashSecret(code), otpTTL.Seconds()); err != nil {
			return httperr.ErrInternalServerError
		}
		if err := mailer.SendOTP(ctx, req.Email, code); err != nil {
			log.Printf("[auth] otp resend failed for %s: %v", req.Email, err)
			return httperr.ErrInternalServerError
		}
		return c.JSON(fiber.Map{"sent": true, "method": "email"})
	}
}

// OTPVerifyHandler trades a correct code for the same payload login would
// have handed out (via sessionPayload, so the two can never drift): one store
// → a tenant JWT; several → the store_pick ticket and the list.
func OTPVerifyHandler(pool *pgxpool.Pool, secret string, mailer Mailer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if mailer == nil {
			return errVerificationUnavailable
		}
		var req struct {
			Email string `json:"email"`
			Code  string `json:"code"`
		}
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		req.Code = strings.TrimSpace(req.Code)
		if req.Email == "" || req.Code == "" {
			return httperr.C(fiber.StatusBadRequest, "email, code required")
		}

		ctx := c.Context()
		guesses, err := hourlyGuesses(ctx, pool, req.Email)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if guesses >= otpHourlyCap {
			return httperr.New(fiber.StatusTooManyRequests, "too_many_attempts",
				"Too many incorrect attempts — try again in an hour.")
		}

		row, ok, err := fetchLiveOTP(ctx, pool, req.Email)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if !ok {
			return httperr.BadRequest("invalid_code", "Invalid or expired code.")
		}
		if row.Attempts >= otpMaxAttempts {
			consumeCode(ctx, pool, row.ID)
			return httperr.C(fiber.StatusBadRequest, "too many incorrect attempts — request a new code")
		}

		var stored string
		if err := pool.QueryRow(ctx,
			`SELECT code_hash FROM auth_verification_codes WHERE id = $1`,
			row.ID).Scan(&stored); err != nil {
			return httperr.ErrInternalServerError
		}

		if !secretMatches(stored, req.Code) {
			// The increment and the 5th-attempt consume share one guarded
			// statement so two racing wrong guesses cannot overshoot silently.
			var attempts int
			err := pool.QueryRow(ctx, `
				UPDATE auth_verification_codes SET attempts = attempts + 1
				WHERE id = $1 AND consumed_at IS NULL
				RETURNING attempts`, row.ID).Scan(&attempts)
			if err == pgx.ErrNoRows {
				return httperr.BadRequest("invalid_code", "Invalid or expired code.")
			}
			if err != nil {
				return httperr.ErrInternalServerError
			}
			if attempts >= otpMaxAttempts {
				consumeCode(ctx, pool, row.ID)
				return httperr.C(fiber.StatusBadRequest, "too many incorrect attempts — request a new code")
			}
			return httperr.BadRequest("invalid_code", "Invalid or expired code.")
		}

		// Guarded consume: if a concurrent verify already took the row, the
		// response is the same invalid_code either way.
		if !consumeCode(ctx, pool, row.ID) {
			return httperr.BadRequest("invalid_code", "Invalid or expired code.")
		}

		payload, err := sessionPayload(ctx, pool, secret, row.AccountID, req.Email)
		if err != nil {
			return err
		}
		return c.JSON(payload)
	}
}

// consumeCode marks one row used. Returns false when the row was already
// consumed — two concurrent verifies of the same code can only succeed once.
func consumeCode(ctx context.Context, pool *pgxpool.Pool, id string) bool {
	tag, err := pool.Exec(ctx, `
		UPDATE auth_verification_codes SET consumed_at = now()
		WHERE id = $1 AND consumed_at IS NULL`, id)
	return err == nil && tag.RowsAffected() == 1
}

// ForgotPasswordHandler sends the reset link. It ALWAYS answers 200 with the
// same body whether or not the email exists — an error only for registered
// addresses would turn the endpoint into an account-enumeration oracle. A send
// failure for a real account is logged, not surfaced, for the same reason.
func ForgotPasswordHandler(pool *pgxpool.Pool, mailer Mailer) fiber.Handler {
	return func(c *fiber.Ctx) error {
		if mailer == nil {
			return errVerificationUnavailable
		}
		var req struct {
			Email string `json:"email"`
		}
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		req.Email = strings.ToLower(strings.TrimSpace(req.Email))
		if req.Email == "" {
			return httperr.C(fiber.StatusBadRequest, "email required")
		}

		ctx := c.Context()
		var accountID string
		err := pool.QueryRow(ctx,
			`SELECT id FROM merchant_accounts WHERE lower(email) = lower($1)`,
			req.Email).Scan(&accountID)
		if err == nil {
			if token, terr := randomToken(); terr == nil {
				if ierr := issueResetToken(ctx, pool, accountID, req.Email, token); ierr != nil {
					log.Printf("[auth] reset token write failed for %s: %v", req.Email, ierr)
				} else if serr := mailer.SendPasswordReset(ctx, req.Email, token); serr != nil {
					log.Printf("[auth] reset email failed for %s: %v", req.Email, serr)
				}
			}
		} else if err != pgx.ErrNoRows {
			return httperr.ErrInternalServerError
		}
		return c.JSON(fiber.Map{"sent": true})
	}
}

// issueResetToken stores the SHA-256 of the raw token, invalidating any live
// predecessor so only the newest link in a user's inbox works.
func issueResetToken(ctx context.Context, pool *pgxpool.Pool, accountID, email, token string) error {
	pruneCodes(ctx, pool, email)
	if _, err := pool.Exec(ctx, `
		UPDATE auth_verification_codes SET consumed_at = now()
		WHERE email = $1 AND purpose = $2 AND consumed_at IS NULL`,
		email, purposePasswordReset); err != nil {
		return err
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO auth_verification_codes (account_id, email, purpose, code_hash, expires_at)
		VALUES ($1, $2, $3, $4, now() + make_interval(secs => $5))`,
		accountID, email, purposePasswordReset, hashSecret(token), passwordResetTTL.Seconds())
	return err
}

// ResetPasswordHandler trades the link's token for a new password and a
// session. The hash is rewritten on merchant_accounts (the row login reads)
// and on every merchant_users mirror — merchant_users is FORCE RLS, so each
// tenant needs its own short transaction with app.current_tenant SET LOCAL,
// same shape as membershipUserID. The account's OTP rows die with the reset:
// a password change ends whatever verification was in flight.
func ResetPasswordHandler(pool *pgxpool.Pool, secret string) fiber.Handler {
	return func(c *fiber.Ctx) error {
		var req struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := c.BodyParser(&req); err != nil {
			return httperr.C(fiber.StatusBadRequest, "invalid body")
		}
		req.Token = strings.TrimSpace(req.Token)
		if req.Token == "" {
			return httperr.BadRequest("invalid_token", "This reset link is invalid or has expired.")
		}
		if len(req.Password) < 8 {
			return httperr.BadRequest("weak_password", "Use at least 8 characters.")
		}
		newHash, err := HashPassword(req.Password)
		if err != nil {
			return httperr.ErrInternalServerError
		}

		ctx := c.Context()
		var codeID, accountID, email string
		err = pool.QueryRow(ctx, `
			SELECT id, account_id, email FROM auth_verification_codes
			WHERE purpose = $1 AND code_hash = $2
			  AND consumed_at IS NULL AND expires_at > now()
			LIMIT 1`,
			purposePasswordReset, hashSecret(req.Token)).
			Scan(&codeID, &accountID, &email)
		if err == pgx.ErrNoRows {
			return httperr.BadRequest("invalid_token", "This reset link is invalid or has expired.")
		}
		if err != nil {
			return httperr.ErrInternalServerError
		}
		if !consumeCode(ctx, pool, codeID) {
			return httperr.BadRequest("invalid_token", "This reset link is invalid or has expired.")
		}

		tx, err := pool.Begin(ctx)
		if err != nil {
			return httperr.ErrInternalServerError
		}
		defer tx.Rollback(ctx)
		if _, err := tx.Exec(ctx,
			`UPDATE merchant_accounts SET password_hash = $2 WHERE id = $1`,
			accountID, newHash); err != nil {
			return httperr.ErrInternalServerError
		}
		// End any OTP still in flight for this account: the password changed.
		if _, err := tx.Exec(ctx, `
			UPDATE auth_verification_codes SET consumed_at = now()
			WHERE account_id = $1 AND purpose = $2 AND consumed_at IS NULL`,
			accountID, purposeOTP); err != nil {
			return httperr.ErrInternalServerError
		}
		if err := tx.Commit(ctx); err != nil {
			return httperr.ErrInternalServerError
		}

		// Mirror onto merchant_users, one RLS-scoped transaction per tenant.
		if err := updateMembershipHashes(ctx, pool, accountID, newHash); err != nil {
			return httperr.ErrInternalServerError
		}

		payload, err := sessionPayload(ctx, pool, secret, accountID, email)
		if err != nil {
			return err
		}
		return c.JSON(payload)
	}
}

// updateMembershipHashes rewrites merchant_users.password_hash for every store
// the account belongs to. Each tenant gets its own transaction because RLS on
// merchant_users is FORCE — there is no ambient scope outside TenantMW.
func updateMembershipHashes(ctx context.Context, pool *pgxpool.Pool, accountID, hash string) error {
	rows, err := pool.Query(ctx,
		`SELECT tenant_id FROM merchant_account_stores WHERE account_id = $1`, accountID)
	if err != nil {
		return err
	}
	var tenants []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			rows.Close()
			return err
		}
		tenants = append(tenants, t)
	}
	rows.Close()
	if rows.Err() != nil {
		return rows.Err()
	}

	for _, tenantID := range tenants {
		tx, err := pool.Begin(ctx)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			"SELECT set_config('app.current_tenant', $1, true)", tenantID); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if _, err := tx.Exec(ctx, `
			UPDATE merchant_users SET password_hash = $3
			WHERE account_id = $1 AND tenant_id = $2`,
			accountID, tenantID, hash); err != nil {
			tx.Rollback(ctx)
			return err
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
	}
	return nil
}

// sessionPayload is the single place a completed account authentication is
// turned into a response: one store → a tenant JWT; several → no JWT, only a
// short-lived store_pick ticket plus the list, so a store can't be chosen
// without a membership row saying the account may act on it. Shared by login,
// OTP verify and password reset so the three can never hand out different
// shapes for the same event.
func sessionPayload(ctx context.Context, pool *pgxpool.Pool, secret, accountID, email string) (fiber.Map, error) {
	stores, err := listStores(ctx, pool, accountID)
	if err != nil {
		return nil, httperr.ErrInternalServerError
	}
	if len(stores) == 0 {
		return nil, httperr.C(fiber.StatusForbidden, "this account has no store yet")
	}

	payload := fiber.Map{
		"user":   fiber.Map{"id": accountID, "email": email},
		"stores": storeListJSON(stores),
	}

	if len(stores) == 1 {
		s := stores[0]
		// The JWT's user_id must be the tenant-scoped merchant_users row id —
		// admin routes key off it — so resolve it before signing.
		userID, err := membershipUserID(ctx, pool, accountID, s.TenantID)
		if err != nil {
			return nil, httperr.ErrInternalServerError
		}
		onboarding := s.OnboardingCompleted
		token, err := SignMerchant(secret, s.TenantID, userID, accountID, s.Role, &onboarding, 24*time.Hour)
		if err != nil {
			return nil, httperr.ErrInternalServerError
		}
		payload["token"] = token
		payload["user"] = fiber.Map{"id": accountID, "email": email, "role": s.Role}
		payload["store"] = storeJSON(s)
		payload["onboarding_completed"] = s.OnboardingCompleted
		payload["expires"] = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
		return payload, nil
	}

	ticket, err := SignStorePick(secret, accountID, 10*time.Minute)
	if err != nil {
		return nil, httperr.ErrInternalServerError
	}
	payload["store_pick_token"] = ticket
	return payload, nil
}

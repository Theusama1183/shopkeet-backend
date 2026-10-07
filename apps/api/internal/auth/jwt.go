package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the payload signed into every Shopkeet JWT. tenant_id is the
// anchor claim — the middleware feeds it to Postgres as app.current_tenant so
// RLS gates every query in the request's transaction. Handlers must never trust
// anything signed without a signature check; the payload carries three mutually
// exclusive identities:
//
//   - merchant tokens: scope="merchant", user_id + role (the tenant's staff)
//   - customer tokens: scope="customer", customer_id (the tenant's shoppers)
//   - affiliate tokens: scope="affiliate", affiliate_id (the tenant's partners)
//
// plus two auth-flow tokens that are not identities in themselves:
//
//   - store_pick: scope="store_pick", account_id only. Minted after login when an
//     account owns more than one store, so the browser can ask for the store list
//     and exchange it for a tenant token without re-sending the password.
//   - otp_bypass: scope="otp_bypass", account_id only. Minted after a successful
//     OTP verification so the same browser can later log in with password alone.
//     NOT signed with the plain secret — see SignOTPBypass.
//
// Middleware checks scope per route group so a customer token can never pass an
// Admin check and an affiliate token can never pass a merchant- or
// customer-scoped route.
type Claims struct {
	TenantID   string `json:"tenant_id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	CustomerID string `json:"customer_id"`
	AffiliateID string `json:"affiliate_id"`
	// AccountID is the merchant account (email identity) the token belongs to.
	// Empty on customer/affiliate tokens. Present on merchant and store_pick
	// tokens so a signed-in merchant can list and switch stores.
	AccountID string `json:"account_id,omitempty"`
	// OnboardingCompleted is a POINTER on purpose. Tokens minted before the
	// one-time wizard existed — and every token from Sign, which many tests
	// still use — decode to nil, and nil means "already onboarded". Only the
	// signup path sets an explicit false, so the admin guard never bounces an
	// existing merchant into the wizard on a missing claim.
	OnboardingCompleted *bool  `json:"onboarding_completed,omitempty"`
	Scope               string `json:"scope"`
	jwt.RegisteredClaims
}

// Sign mints a merchant-scoped tenant JWT. ttl is usually 24h. It is the
// no-onboarding, no-account form kept for existing callers (and tests); new auth
// paths use SignMerchant so the guard can see the wizard state.
func Sign(secret, tenantID, userID, role string, ttl time.Duration) (string, error) {
	return SignMerchant(secret, tenantID, userID, "", role, nil, ttl)
}

// SignMerchant mints a merchant-scoped tenant JWT carrying the account identity
// and the store's onboarding state. Pass onboarding == nil for a token whose
// onboarding state is unknown (treated as onboarded).
func SignMerchant(secret, tenantID, userID, accountID, role string, onboarding *bool, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantID:            tenantID,
		UserID:              userID,
		Role:                role,
		AccountID:           accountID,
		OnboardingCompleted: onboarding,
		Scope:               "merchant",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "shopkeet",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// SignStorePick mints the short-lived ticket that stands in for the password
// between a successful login and the merchant picking one of their stores. It
// carries an account id and nothing else: no tenant, no role, so it cannot be
// used to act on a store until SelectStore exchanges it for a merchant token.
// ttl is deliberately short (minutes, not hours).
func SignStorePick(secret, accountID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		AccountID: accountID,
		Scope:     "store_pick",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "shopkeet",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// OTPBypassKey derives the per-account "trusted device" signing key from the
// global JWT secret and the account's CURRENT password hash. Binding the ticket
// to the hash means a password change (or reset) invalidates every outstanding
// bypass — a reset can't leave unlocked browsers behind. Feeding two secrets
// through HMAC-SHA256 leaks no structure about either.
func OTPBypassKey(secret, passwordHash string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte("otp-bypass:" + passwordHash))
	return mac.Sum(nil)
}

// SignOTPBypass mints the long-lived "remember this browser" ticket a verified
// OTP leaves behind: scope="otp_bypass", account_id only — no tenant, no role,
// so it cannot act on a store. It exists solely to let a later password login
// skip the emailed code when the same browser already proved the mailbox once.
// It is signed under the account's current password hash (see OTPBypassKey),
// which is why the hash has to be passed in — the caller has already fetched it.
func SignOTPBypass(secret, accountID, passwordHash string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		AccountID: accountID,
		Scope:     "otp_bypass",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "shopkeet",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(OTPBypassKey(secret, passwordHash))
}

// ParseOTPBypass verifies a trusted-device ticket against the account's current
// password hash and returns the account id. Any deviation — bad signature,
// expired, wrong scope, or a password change since minting — yields ("", false),
// so callers fall back to the full OTP gate.
func ParseOTPBypass(secret, passwordHash, token string) (string, bool) {
	if token == "" || passwordHash == "" {
		return "", false
	}
	claims := &Claims{}
	_, err := jwt.ParseWithClaims(token, claims, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return OTPBypassKey(secret, passwordHash), nil
	})
	if err != nil || claims.Scope != "otp_bypass" || claims.AccountID == "" {
		return "", false
	}
	return claims.AccountID, true
}

// SignCustomer mints a customer-scoped JWT (Phase 11). It carries customer_id
// instead of user_id/role, and scope="customer"; admin middleware rejects it.
func SignCustomer(secret, tenantID, customerID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantID:   tenantID,
		CustomerID: customerID,
		Scope:      "customer",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "shopkeet",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// SignAffiliate mints an affiliate-scoped JWT (Phase 27). It carries
// affiliate_id instead of user_id/role or customer_id, and scope="affiliate";
// merchant and customer middleware both reject it.
func SignAffiliate(secret, tenantID, affiliateID string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantID:    tenantID,
		AffiliateID: affiliateID,
		Scope:       "affiliate",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "shopkeet",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
}

// Parse verifies a JWT signature against secret and returns the claims.
func Parse(secret, token string) (*Claims, error) {
	c := &Claims{}
	_, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return []byte(secret), nil
	})
	if err != nil {
		return nil, err
	}
	return c, nil
}

// ParseStorePickAuthorizes reports whether token may act on behalf of an account
// for store selection, and returns that account id. It accepts two token kinds:
// the store_pick ticket minted by login, and an already-issued merchant token —
// so a signed-in merchant can switch stores from the sidebar without re-entering
// a password. A token with no account id, or any other scope (customer,
// affiliate), is refused.
func ParseStorePickAuthorizes(secret, token string) (string, bool) {
	claims, err := Parse(secret, token)
	if err != nil || claims.AccountID == "" {
		return "", false
	}
	switch claims.Scope {
	case "store_pick", "merchant":
		return claims.AccountID, true
	}
	return "", false
}

// NeedsOnboarding reports whether this token's bearer must still run the one-time
// store wizard. Only an explicit onboarding_completed=false qualifies; a nil claim
// (an older token, or one from Sign) counts as onboarded.
func NeedsOnboarding(claims *Claims) bool {
	return claims.OnboardingCompleted != nil && !*claims.OnboardingCompleted
}

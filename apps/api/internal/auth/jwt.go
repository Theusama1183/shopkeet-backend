package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the payload signed into every Shopkeet JWT. tenant_id is the
// anchor claim — the middleware feeds it to Postgres as app.current_tenant so
// RLS gates every query in the request's transaction. Handlers must never trust
// anything signed without a signature check; the payload carries two mutually
// exclusive identities:
//
//   - merchant tokens: scope="merchant", user_id + role (the tenant's staff)
//   - customer tokens: scope="customer", customer_id (the tenant's shoppers)
//
// Middleware checks scope per route group so a customer token can never pass an
// Admin check and a merchant token can never pass a customer-scoped route.
type Claims struct {
	TenantID   string `json:"tenant_id"`
	UserID     string `json:"user_id"`
	Role       string `json:"role"`
	CustomerID string `json:"customer_id"`
	Scope      string `json:"scope"`
	jwt.RegisteredClaims
}

// Sign mints a merchant-scoped tenant JWT. ttl is usually 24h.
func Sign(secret, tenantID, userID, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantID: tenantID,
		UserID:   userID,
		Role:     role,
		Scope:    "merchant",
		RegisteredClaims: jwt.RegisteredClaims{
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
			Issuer:    "shopkeet",
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(secret))
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

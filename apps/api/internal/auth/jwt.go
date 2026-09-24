package auth

import (
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Claims is the payload signed into every Shopkeet JWT. tenant_id is the
// anchor claim — the middleware feeds it to Postgres as app.current_tenant so
// RLS gates every query in the request's transaction.
type Claims struct {
	TenantID string `json:"tenant_id"`
	UserID   string `json:"user_id"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// Sign mints a tenant-scoped JWT. ttl is usually 24h for merchant sessions.
func Sign(secret, tenantID, userID, role string, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := Claims{
		TenantID: tenantID,
		UserID:   userID,
		Role:     role,
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

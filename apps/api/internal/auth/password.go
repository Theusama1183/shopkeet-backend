package auth

import "golang.org/x/crypto/bcrypt"

// HashPassword returns a bcrypt hash of p. Cost 12 is the default from
// x/crypto — solid for login-rate traffic without signup-time lag spikes.
func HashPassword(p string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(p), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword reports whether p matches the stored bcrypt hash.
func CheckPassword(p, hash string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(p)) == nil
}

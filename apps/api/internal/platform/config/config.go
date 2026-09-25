package config

import (
	"fmt"
	"os"
)

// Config holds all runtime configuration loaded from environment variables.
type Config struct {
	DatabaseURL   string
	RedisURL      string
	Port          string
	JWTSecret     string
	AppBaseDomain string

	// MetricsToken gates GET /metrics (Phase 7). When empty the endpoint is
	// still served but is only safe behind a network-level proxy rule.
	MetricsToken string

	// Cloudflare R2 (object storage — Phase 2). R2_PUBLIC_URL is the public
	// base (r2.dev or custom domain) that media URLs are built from.
	R2AccountID   string
	R2AccessKeyID string
	R2SecretKey   string
	R2BucketName  string
	R2PublicURL   string

	// Resend (transactional email — Phase 12). Both must be set together to enable;
	// if unset, a log-only provider is used.
	ResendAPIKey           string
	NotificationsFromEmail string
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		RedisURL:               os.Getenv("REDIS_URL"),
		Port:                   os.Getenv("PORT"),
		JWTSecret:              os.Getenv("JWT_SECRET"),
		AppBaseDomain:          os.Getenv("APP_BASE_DOMAIN"),
		MetricsToken:           os.Getenv("METRICS_TOKEN"),
		R2AccountID:            os.Getenv("R2_ACCOUNT_ID"),
		R2AccessKeyID:          os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretKey:            os.Getenv("R2_SECRET_ACCESS_KEY"),
		R2BucketName:           os.Getenv("R2_BUCKET_NAME"),
		R2PublicURL:            os.Getenv("R2_PUBLIC_URL"),
		ResendAPIKey:           os.Getenv("RESEND_API_KEY"),
		NotificationsFromEmail: os.Getenv("NOTIFICATIONS_FROM_EMAIL"),
	}

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.Port == "" {
		c.Port = "3001"
	}

	// R2 is required together: either all of account/creds/bucket/public URL
	// are configured (media enabled) or none of them is.
	hasAny := c.R2AccountID != "" || c.R2AccessKeyID != "" || c.R2SecretKey != "" ||
		c.R2BucketName != "" || c.R2PublicURL != ""
	hasAll := c.R2AccountID != "" && c.R2AccessKeyID != "" && c.R2SecretKey != "" &&
		c.R2BucketName != "" && c.R2PublicURL != ""
	if hasAny && !hasAll {
		return nil, fmt.Errorf("R2_ACCOUNT_ID, R2_ACCESS_KEY_ID, R2_SECRET_ACCESS_KEY, R2_BUCKET_NAME, R2_PUBLIC_URL must be set together")
	}

	return c, nil
}

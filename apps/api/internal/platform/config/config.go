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
}

// Load reads configuration from the environment.
func Load() (*Config, error) {
	c := &Config{
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		RedisURL:      os.Getenv("REDIS_URL"),
		Port:          os.Getenv("PORT"),
		JWTSecret:     os.Getenv("JWT_SECRET"),
		AppBaseDomain: os.Getenv("APP_BASE_DOMAIN"),
	}

	if c.DatabaseURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}
	if c.Port == "" {
		c.Port = "3001"
	}

	return c, nil
}
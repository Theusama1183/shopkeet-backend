package main

import (
	"context"
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/joho/godotenv"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/platform/config"
	"github.com/shopkeet/api/internal/platform/db"
)

func main() {
	// .env is optional; real environments provide vars directly.
	_ = godotenv.Load()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("invalid configuration: %v", err)
	}

	ctx := context.Background()
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}
	defer pool.Close()

	app := fiber.New()

	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.Status(200).JSON(fiber.Map{"status": "ok"})
	})

	// Phase 1 — tenants & auth. RegisterRoutes mounts the public signup/login
	// surface plus the tenant-scoped /api/v1/me behind a request transaction
	// that SET LOCALs app.current_tenant so Postgres RLS scopes every query.
	auth.RegisterRoutes(app, pool, cfg.JWTSecret)

	log.Printf("Shopkeet API listening on :%s", cfg.Port)
	if err := app.Listen(":" + cfg.Port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

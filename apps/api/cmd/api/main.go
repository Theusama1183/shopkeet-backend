package main

import (
	"context"
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/joho/godotenv"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/media"
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

	// Phase 1 & 2 — auth + media share the /api/v1 group. auth.RegisterRoutes
	// mounts the public signup/login; media.RegisterRoutes adds the tenant-
	// scoped R2 media library behind TenantMW (JWT + SET LOCAL app.current_tenant).
	v1 := app.Group("/api/v1", logger.New())
	auth.RegisterRoutes(v1, pool, cfg.JWTSecret)

	var mediaSvc *media.Service
	if cfg.R2AccountID != "" {
		r2, err := media.NewR2(cfg.R2AccountID, cfg.R2AccessKeyID, cfg.R2SecretKey,
			cfg.R2BucketName, media.PresignTTL)
		if err != nil {
			log.Fatalf("failed to init R2: %v", err)
		}
		mediaSvc = media.New(pool, r2, cfg.R2PublicURL, media.PresignTTL)
		media.RegisterRoutes(v1, pool, cfg.JWTSecret, mediaSvc)
	} else {
		log.Println("R2 not configured; /media routes not mounted")
	}

	log.Printf("Shopkeet API listening on :%s", cfg.Port)
	if err := app.Listen(":" + cfg.Port); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

package main

import (
	"context"
	"log"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/joho/godotenv"

	"github.com/shopkeet/api/internal/auth"
	"github.com/shopkeet/api/internal/cart"
	"github.com/shopkeet/api/internal/catalog"
	"github.com/shopkeet/api/internal/content"
	"github.com/shopkeet/api/internal/customers"
	"github.com/shopkeet/api/internal/discounts"
	"github.com/shopkeet/api/internal/media"
	"github.com/shopkeet/api/internal/notifications"
	"github.com/shopkeet/api/internal/orders"
	"github.com/shopkeet/api/internal/payments"
	"github.com/shopkeet/api/internal/platform/config"
	"github.com/shopkeet/api/internal/platform/db"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/observe"
	"github.com/shopkeet/api/internal/shipping"
	"github.com/shopkeet/api/internal/tenants"
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

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})

	// Phase 7 — observable by default. Panics -> standard 500 error shape,
	// every request gets a traceable id + JSON access log, and request volume/
	// latency land in the Prometheus registry (served at /metrics, token-gated).
	metrics := observe.NewMetrics(pool)
	app.Use(recover.New(recover.Config{EnableStackTrace: false}))
	app.Use(observe.RequestID)
	app.Use(observe.AccessLog)
	app.Use(metrics.Middleware)

	app.Get("/healthz", func(c *fiber.Ctx) error {
		return c.Status(200).JSON(fiber.Map{"status": "ok"})
	})

	// /metrics is always mounted so a firewall/proxy rule alone can expose it
	// (docs/api-reference.md §Platform marks it Internal). When METRICS_TOKEN is
	// configured, the endpoint additionally requires Authorization: Bearer.
	app.Get("/metrics", func(c *fiber.Ctx) error {
		if cfg.MetricsToken != "" && c.Get("Authorization") != "Bearer "+cfg.MetricsToken {
			return httperr.Unauthorized("unauthorized", "invalid or missing bearer token")
		}
		return metrics.Handler()(c)
	})

	// Phase 1 & 2 — auth + media share the /api/v1 group. auth.RegisterRoutes
	// mounts the public signup/login; media.RegisterRoutes adds the tenant-
	// scoped R2 media library behind TenantMW (JWT + SET LOCAL app.current_tenant).
	v1 := app.Group("/api/v1")
	// New tenants get their storefront chrome (home page post, the required
	// templates, header/footer sections) inside the signup transaction.
	auth.RegisterTenantCreatedHook(content.SeedDefaults)
	auth.RegisterRoutes(v1, pool, cfg.JWTSecret)

	// Phase 6 — content & page builder. Placeholder JSON feeds the Puck editor;
	// onPublish saves the Puck layout verbatim through the admin endpoints.
	content.RegisterRoutes(v1, pool, cfg.JWTSecret, content.New(pool))

	// Phase 3 — catalog. Storefront routes resolve the tenant from the
	// X-Tenant-ID header (Next.js middleware per docs/03-architecture.md §2);
	// admin routes use the JWT via TenantMW. RLS scopes everything.
	catalog.RegisterRoutes(v1, pool, cfg.JWTSecret, catalog.New(pool))

	// Phase 4 — guest carts. RLS scopes rows by the tenant resolved from
	// X-Tenant-ID (auth.CustomerMW); the customer_session (cookie/header) keys
	// the cart within it. Stock arbitration happens at checkout (Phase 5); the
	// Redis reserver is a best-effort fast path only (never authoritative).
	var reserver cart.Reserver = cart.NoopReserver{}
	if cfg.RedisURL != "" {
		rr, err := cart.NewRedisReserver(cfg.RedisURL)
		if err != nil {
			log.Fatalf("failed to init redis reserver: %v", err)
		}
		defer rr.Close()
		reserver = rr
		log.Printf("cart unit reservation via Redis at %s", cfg.RedisURL)
	}
	cart.RegisterRoutes(v1, pool, cart.New(pool, reserver))

	// Phase 5 — checkout & orders (COD). The payments registry has one provider
	// (cod); events surface order.created / order.paid for future webhooks.
	bus := events.NewBus()
	bus.Subscribe("order.created", func(ctx context.Context, e events.Event) error {
		log.Printf("event order.created: %+v", e.Data)
		return nil
	})
	bus.Subscribe("order.paid", func(ctx context.Context, e events.Event) error {
		log.Printf("event order.paid: %+v", e.Data)
		return nil
	})
	orders.RegisterRoutes(v1, pool, cfg.JWTSecret,
		orders.New(pool, bus, payments.NewRegistry()))

	// Phase 9 — shipping zones/rates. The public GET /shipping/rates feeds the
	// checkout form; checkout snapshots the resolved rate into the order.
	shipping.RegisterRoutes(v1, pool, cfg.JWTSecret, shipping.New(pool))

	// Phase 10 — discounts. Admin CRUD behind TenantMW; the cart apply endpoint
	// lives in the cart package and checkout claims the usage atomically.
	discounts.RegisterRoutes(v1, pool, cfg.JWTSecret, discounts.New(pool))

	// Phase 11 — customer accounts. Signup/login are storefront-public; the
	// /me group (profile, order history, saved addresses) requires a
	// customer-scoped JWT. Checkout under CustomerOrGuestMW links orders to the
	// account when the caller is signed in, and stays fully guest otherwise.
	customers.RegisterRoutes(v1, pool, cfg.JWTSecret, customers.New(pool, cfg.JWTSecret, bus))

	// Phase 12 — notifications. Subscribe to the internal event bus; sends
	// order confirmations, delivery updates, and welcome emails via Resend
	// (if configured) or logs to stdout. A failed send never fails checkout.
	var notifProv notifications.Provider = notifications.LogProvider{}
	if cfg.ResendAPIKey != "" && cfg.NotificationsFromEmail != "" {
		notifProv = notifications.NewResendProvider(cfg.ResendAPIKey, cfg.NotificationsFromEmail)
		log.Printf("notifications: using Resend provider")
	} else {
		log.Printf("notifications: using log provider (set RESEND_API_KEY + NOTIFICATIONS_FROM_EMAIL to enable email)")
	}
	notifSvc := notifications.New(pool, notifProv)
	notifSvc.Subscribe(bus)
	notifications.RegisterRoutes(v1, pool, cfg.JWTSecret)

	// Phase 13 — store settings + tax. Admin GET/PATCH /tenant/settings.
	tenants.RegisterRoutes(v1, pool, cfg.JWTSecret)

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

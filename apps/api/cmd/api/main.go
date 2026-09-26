package main

import (
	"context"
	"log"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/recover"
	"github.com/hibiken/asynq"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"

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
	"github.com/shopkeet/api/internal/platform/cache"
	"github.com/shopkeet/api/internal/platform/db"
	"github.com/shopkeet/api/internal/platform/events"
	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/idempotency"
	"github.com/shopkeet/api/internal/platform/observe"
	"github.com/shopkeet/api/internal/platform/queue"
	"github.com/shopkeet/api/internal/platform/ratelimit"
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

	// Phase 14 — Redis-backed reliability layer: the per-route rate limiter
	// and the Asynq job worker share the same Redis the cart Reserver uses.
	// Redis is a cache/queue only; Postgres stays the source of truth.
	// When REDIS_URL is absent, limiting is disabled and the Asynq worker
	// never starts (the API still serves; it just can't enqueue/dequeue jobs).
	var rdb *redis.Client
	var worker *queue.Worker
	if cfg.RedisURL != "" {
		ropt, err := redis.ParseURL(cfg.RedisURL)
		if err != nil {
			log.Fatalf("failed to parse redis url: %v", err)
		}
		rdb = redis.NewClient(ropt)
		defer rdb.Close()

		// Asynq worker: registers the idempotency-key purge (Phase 14) as a
		// scheduled hourly job, drains Redis-backed job queues in-process.
		w, err := queue.NewWorker(cfg.RedisURL, 4)
		if err != nil {
			log.Fatalf("failed to init asynq worker: %v", err)
		}
		worker = w
		// Purge idempotency rows older than 24h hourly (docs/08-hardening… §14).
		worker.Register(queue.TaskTypeIdempotencyPurge,
			purgeIdempotencyHandler(pool))
		if err := worker.RegisterPeriodic("@every 1h",
			asynq.NewTask(queue.TaskTypeIdempotencyPurge, nil)); err != nil {
			log.Fatalf("failed to schedule idempotency purge: %v", err)
		}
		worker.Start()
		log.Printf("redis rate limiting + asynq worker enabled at %s", cfg.RedisURL)
	}
	defer func() {
		if worker != nil {
			worker.Stop()
		}
	}()

	// Rate limiter (nil-safe: disabling when Redis is absent).
	rl := ratelimit.New(rdb)

	// Phase 14 (Redis hot-data layer) — cache-aside for product detail: same
	// Redis client the limiter holds. cache.Noop{} when REDIS_URL is absent.
	var cca cache.Cache = cache.Noop{}
	if rdb != nil {
		cca = cache.NewRedisClient(rdb)
	}

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
	auth.RegisterRoutes(v1, pool, cfg.JWTSecret, rl)

	// Phase 6 — content & page builder. Placeholder JSON feeds the Puck editor;
	// onPublish saves the Puck layout verbatim through the admin endpoints.
	content.RegisterRoutes(v1, pool, cfg.JWTSecret, content.New(pool))

	// Phase 3 — catalog. Storefront routes resolve the tenant from the
	// X-Tenant-ID header (Next.js middleware per docs/03-architecture.md §2);
	// admin routes use the JWT via TenantMW. RLS scopes everything.
	catalogSvc := catalog.New(pool)
	catalogSvc.SetCache(cca)
	catalog.RegisterRoutes(v1, pool, cfg.JWTSecret, catalogSvc)

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
	}
	cart.RegisterRoutes(v1, pool, cart.New(pool, reserver), rl)

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
	ordersSvc := orders.New(pool, bus, payments.NewRegistry())
	ordersSvc.SetCache(cca)
	orders.RegisterRoutes(v1, pool, cfg.JWTSecret,
		ordersSvc, rl)

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
	customers.RegisterRoutes(v1, pool, cfg.JWTSecret, customers.New(pool, cfg.JWTSecret, bus), rl)

	// Phase 12 — notifications. Subscribe to the internal event bus; sends
	// order confirmations, delivery updates, and welcome emails. Provider
	// priority: SMTP (when SMTP_HOST is set) > Resend (when RESEND_API_KEY is
	// set) > log-to-stdout. A failed send never fails checkout.
	var notifProv notifications.Provider = notifications.LogProvider{}
	fromEmail := cfg.SMTPFromEmail
	if fromEmail == "" {
		fromEmail = cfg.NotificationsFromEmail
	}
	switch {
	case cfg.SMTPHost != "":
		notifProv = notifications.NewSMTPProvider(notifications.SMTPConfig{
			Host:       cfg.SMTPHost,
			Port:       cfg.SMTPPort,
			Username:   cfg.SMTPUsername,
			Password:   cfg.SMTPPassword,
			From:       fromEmail,
			TLSMode:    cfg.SMTPTLSMode,
			SkipVerify: !cfg.SMTPTLSVerify,
		})
		log.Printf("notifications: using SMTP provider at %s:%s (tls=%s)", cfg.SMTPHost, orDefault(cfg.SMTPPort, "587"), orDefault(cfg.SMTPTLSMode, "auto"))
	case cfg.ResendAPIKey != "" && cfg.NotificationsFromEmail != "":
		notifProv = notifications.NewResendProvider(cfg.ResendAPIKey, cfg.NotificationsFromEmail)
		log.Printf("notifications: using Resend provider")
	default:
		log.Printf("notifications: using log provider (set SMTP_HOST or RESEND_API_KEY + NOTIFICATIONS_FROM_EMAIL to enable email)")
	}
	notifSvc := notifications.New(pool, notifProv, cfg.AppBaseDomain)
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

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// purgeIdempotencyHandler is the Asynq task for the hourly idempotency-key
// sweep. It deletes keys older than idempotency.Retention; failures are logged
// and re-queued by Asynq's retry (MaxRetry set at enqueue time).
func purgeIdempotencyHandler(pool *pgxpool.Pool) func(context.Context, *asynq.Task) error {
	return func(ctx context.Context, _ *asynq.Task) error {
		n, err := idempotency.PurgeExpired(ctx, pool, time.Now().Add(-idempotency.Retention))
		if err != nil {
			log.Printf("idempotency purge failed: %v", err)
			return err
		}
		if n > 0 {
			log.Printf("idempotency purge removed %d expired keys", n)
		}
		return nil
	}
}

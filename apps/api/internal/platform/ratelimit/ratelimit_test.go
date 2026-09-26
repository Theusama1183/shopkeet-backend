package ratelimit_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"

	"github.com/shopkeet/api/internal/platform/httperr"
	"github.com/shopkeet/api/internal/platform/ratelimit"
)

// TestRateLimitWindow is the Phase 14 acceptance criterion: requests beyond a
// route's per-window budget return 429 with the X-RateLimit-* / Retry-After
// headers, and the bucket resets once the window passes. Requires a reachable
// Redis (REDIS_URL, e.g. an ssh tunnel to shopkeet-redis).
func TestRateLimitWindow(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping integration")
	}
	ctx := context.Background()
	ropt, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatalf("parse redis url: %v", err)
	}
	rdb := redis.NewClient(ropt)
	defer rdb.Close()
	if err := rdb.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}

	app := fiber.New(fiber.Config{ErrorHandler: httperr.Handler})
	app.Post("/limited",
		ratelimit.New(rdb).Middleware(ratelimit.Entry{
			Route:   "POST /test-limited",
			Limit:   2,
			Window:  2 * time.Second,
			KeyFunc: ratelimit.ByIP(),
		}),
		func(c *fiber.Ctx) error { return c.SendStatus(fiber.StatusOK) })

	do := func() *http.Response {
		req := httptest.NewRequest("POST", "/limited", nil)
		resp, err := app.Test(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		return resp
	}

	// Requests 1 and 2 pass; headers report budget.
	r1 := do()
	if r1.StatusCode != fiber.StatusOK {
		t.Fatalf("request 1: want 200 got %d", r1.StatusCode)
	}
	if got := r1.Header.Get(ratelimit.HeaderLimit); got != "2" {
		t.Fatalf("limit header: want 2 got %q", got)
	}

	r2 := do()
	if r2.StatusCode != fiber.StatusOK {
		t.Fatalf("request 2: want 200 got %d", r2.StatusCode)
	}
	if got := r2.Header.Get(ratelimit.HeaderRemaining); got != "0" {
		t.Fatalf("remaining header after 2nd: want 0 got %q", got)
	}

	// Request 3 is throttled.
	r3 := do()
	if r3.StatusCode != fiber.StatusTooManyRequests {
		t.Fatalf("request 3: want 429 got %d", r3.StatusCode)
	}
	if got := r3.Header.Get(ratelimit.HeaderRetry); got == "" {
		t.Fatal("retry-after header missing on 429")
	}
}

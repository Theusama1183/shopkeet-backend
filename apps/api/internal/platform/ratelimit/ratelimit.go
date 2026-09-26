package ratelimit

import (
	"fmt"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/redis/go-redis/v9"

	"github.com/shopkeet/api/internal/platform/httperr"
)

// Response headers per acceptance: a 429 carries these so the storefront/admin
// can back off without guessing.
const (
	HeaderLimit     = "X-RateLimit-Limit"
	HeaderRemaining = "X-RateLimit-Remaining"
	HeaderReset     = "X-RateLimit-Reset"
	HeaderRetry     = "Retry-After"
)

// Entry is one per-route limit (docs/08-hardening-and-features-build-spec.md
// Phase 14 table). A login brute-force and a checkout burst are different
// problems, so limits are applied per route — never one global bucket.
type Entry struct {
	Route  string        // request path, used only for auditing/logging
	Limit  int           // max requests per window
	Window time.Duration // the window
	// KeyFunc renders the bucketing key from the request (IP, IP+email,
	// cart/session, ...).
	KeyFunc func(c *fiber.Ctx) string
}

// Limiter is a fixed-window rate limiter over Redis. It implements the same
// guarantee the rest of the stack holds for Redis: it is a cache, never a
// source of truth. If Redis is unreachable the request proceeds (fail-open),
// matching how the cart Reserver never fails a cart write on Redis error.
type Limiter struct {
	rdb *redis.Client
}

// New builds a Limiter. A nil client disables limiting entirely (used when
// REDIS_URL is absent, exactly like cart.NoopReserver).
func New(rdb *redis.Client) *Limiter {
	return &Limiter{rdb: rdb}
}

// Middleware returns a Fiber handler enforcing one rate-limit entry. The
// request is rejected with 429 when the window bucket is exhausted; all
// responses carry the X-RateLimit-* headers so callers stay informed.
func (l *Limiter) Middleware(e Entry) fiber.Handler {
	windowSecs := int64(e.Window.Seconds())
	if windowSecs < 1 {
		windowSecs = 1
	}
	return func(c *fiber.Ctx) error {
		if l == nil || l.rdb == nil {
			return c.Next()
		}
		key := "shopkeet:rl:" + e.Route + ":" + e.KeyFunc(c)
		if key == "shopkeet:rl::" {
			return c.Next()
		}

		ctx := c.Context()
		// INCR then set the TTL on first hit — atomic fixed window. On Redis
		// failure we fail open (the request is allowed; the next one retries).
		count, err := l.rdb.Incr(ctx, key).Result()
		if err != nil {
			return c.Next()
		}
		if count == 1 {
			if err := l.rdb.Expire(ctx, key, e.Window).Err(); err != nil {
				return c.Next()
			}
		}
		ttl, err := l.rdb.TTL(ctx, key).Result()
		if err != nil {
			return c.Next()
		}

		remaining := e.Limit - int(count)
		if remaining < 0 {
			remaining = 0
		}
		reset := int64(e.Window.Seconds())
		if ttl > 0 {
			reset = int64(ttl.Seconds())
		}

		c.Set(HeaderLimit, strconv.Itoa(e.Limit))
		c.Set(HeaderRemaining, strconv.Itoa(remaining))
		c.Set(HeaderReset, strconv.FormatInt(time.Now().Unix()+reset, 10))
		c.Set(HeaderRetry, strconv.FormatInt(reset, 10))

		if int(count) > e.Limit {
			return httperr.New(fiber.StatusTooManyRequests, "rate_limited",
				"too many requests, slow down")
		}
		return c.Next()
	}
}

// --- key builders -------------------------------------------------------------

// ByIP buckets on the client address alone (signup, checkout floods).
func ByIP() func(c *fiber.Ctx) string {
	return func(c *fiber.Ctx) string { return c.IP() }
}

// ByIPAndBodyField buckets on IP plus a literal from the JSON body (email),
// so brute-forcing one account does not exhaust the whole IP's budget.
func ByIPAndBodyField(field string) func(c *fiber.Ctx) string {
	return func(c *fiber.Ctx) string {
		ip := c.IP()
		email := ""
		var m map[string]any
		if err := c.BodyParser(&m); err == nil {
			if v, ok := m[field].(string); ok {
				email = v
			}
		}
		return fmt.Sprintf("%s::%s", ip, email)
	}
}

// BySession buckets on the guest/signed-in cart session (cart operations).
func BySession() func(c *fiber.Ctx) string {
	return func(c *fiber.Ctx) string {
		if s, ok := c.Locals("customer_session").(string); ok {
			return s
		}
		return c.IP()
	}
}
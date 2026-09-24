package cart

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ReserveTTL is how long a unit reservation on add-to-cart survives
// (docs/04-agent-build-spec.md Phase 4: 15-minute TTL).
const ReserveTTL = 15 * time.Minute

// Reserver claims unit "slots" when an item is added to a cart. This is only a
// fast path: the authoritative anti-oversell guard is the checkout-time
// `SELECT ... FOR UPDATE` on products (Phase 5). Therefore reservation is
// strictly best-effort and must never fail a cart operation — if Redis is
// unreachable the op proceeds, exactly as the spec's acceptance wants
// ("one succeeds at checkout, not at add-to-cart time").
type Reserver interface {
	// ReserveUnits tentatively reserves the given quantity of unit slots for a
	// product. Returns nil even on Redis errors (best-effort).
	ReserveUnits(ctx context.Context, productID string, quantity int) error
	Close() error
}

// NoopReserver is used when REDIS_URL is not configured. Checkout stays safe
// because the database is authoritative.
type NoopReserver struct{}

func (NoopReserver) ReserveUnits(context.Context, string, int) error { return nil }
func (NoopReserver) Close() error                                    { return nil }

// RedisReserver SETNXes shopkeet:reserve:{product_id}:{unit_index} for each
// unit, TTL 15 minutes, per the Phase 4 spec. Errors are swallowed by design —
// Redis is a cache, never the source of truth.
type RedisReserver struct {
	rdb *redis.Client
}

// NewRedisReserver builds a RedisReserver from a redis:// URL.
func NewRedisReserver(url string) (*RedisReserver, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &RedisReserver{rdb: redis.NewClient(opt)}, nil
}

func (r *RedisReserver) ReserveUnits(ctx context.Context, productID string, quantity int) error {
	if r == nil || r.rdb == nil || quantity <= 0 {
		return nil
	}
	for i := 0; i < quantity; i++ {
		key := fmt.Sprintf("shopkeet:reserve:%s:%d", productID, i)
		if _, err := r.rdb.SetNX(ctx, key, "cart", ReserveTTL).Result(); err != nil {
			return nil // never fail a cart write because Redis hiccuped
		}
	}
	return nil
}

func (r *RedisReserver) Close() error {
	if r == nil || r.rdb == nil {
		return nil
	}
	return r.rdb.Close()
}

package cache

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Package cache is a thin cache-aside layer over the shared Redis instance
// (docs/03-architecture.md: "cache + cart + Asynq jobs", docs/04-agent-build-
// spec.md: "Redis is a cache/queue layer only — Postgres is always the source
// of truth"). Reads go through Redis when available and fall back to the
// caller's Postgres query; every cached payload must be reconstructable from
// Postgres, so a Redis flush or TTL expiry is never a correctness event.

// Prefix is the shared Redis keyspace namespace (already used by the cart
// reserver via shopkeet:reserve:*). Cache keys build on it:
//
//	shopkeet:cache:{scope}:{id}[:extra]
type Prefix = string

// Key returns a namespaced cache key.
func Key(scope, id string) string {
	return fmt.Sprintf("shopkeet:cache:%s:%s", scope, id)
}

// ProductKey returns the cache key for a product detail entry scoped to a
// tenant and viewer. The two viewer values are "public" (active products,
// what storefronts read) and "admin" (any status, what the merchant edits) —
// they must never share an entry because the JSON differs.
func ProductKey(tid, id, viewer string) string {
	return Key("product", tid+"/"+id+"/"+viewer)
}

// InvalidateProduct drops both public and admin product detail entries. Used
// by catalog mutations and by checkout (inventory refreshes the aggregates the
// cached payload embeds). Safe even when the entry never existed or the
// calling tx rolls back: the next read simply repopulates from Postgres.
func InvalidateProduct(ctx context.Context, c Cache, tid, id string) {
	if c == nil {
		return
	}
	c.Del(ctx, ProductKey(tid, id, "public"))
	c.Del(ctx, ProductKey(tid, id, "admin"))
}

// Cache is the cache-aside surface used by services. Implementations must be
// nil-safe: a missing/unreachable Redis must degrade reads to the Postgres
// path, never fail the request.
type Cache interface {
	// Get returns the cached payload for key (nil,false on miss/error).
	Get(ctx context.Context, key string) ([]byte, bool)
	// Set stores payload under key for ttl. Errors are ignored (best-effort).
	Set(ctx context.Context, key string, payload []byte, ttl time.Duration)
	// Del removes key. Errors are ignored.
	Del(ctx context.Context, key string)
	// DelPrefix removes every key matching shopkeet:cache:{prefix}*. Used for
	// invalidation by scope/id. Errors are ignored.
	DelPrefix(ctx context.Context, prefix string)
	Close() error
}

// Noop disables caching (REDIS_URL unset) — reads always hit Postgres.
type Noop struct{}

func (Noop) Get(context.Context, string) ([]byte, bool)          { return nil, false }
func (Noop) Set(context.Context, string, []byte, time.Duration)  {}
func (Noop) Del(context.Context, string)                         {}
func (Noop) DelPrefix(context.Context, string)                   {}
func (Noop) Close() error                                        { return nil }

// Redis is a go-redis backed cache-aside layer.
type Redis struct {
	rdb *redis.Client
}

// NewRedis builds a Redis cache from a redis:// URL.
func NewRedis(url string) (*Redis, error) {
	opt, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis url: %w", err)
	}
	return &Redis{rdb: redis.NewClient(opt)}, nil
}

// NewRedisClient wraps an existing go-redis client so the cache-aside layer
// shares the limiter's connection instead of opening its own.
func NewRedisClient(rdb *redis.Client) *Redis {
	return &Redis{rdb: rdb}
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool) {
	if r == nil || r.rdb == nil {
		return nil, false
	}
	b, err := r.rdb.Get(ctx, key).Bytes()
	if err != nil {
		return nil, false
	}
	return b, true
}

func (r *Redis) Set(ctx context.Context, key string, payload []byte, ttl time.Duration) {
	if r == nil || r.rdb == nil || ttl <= 0 {
		return
	}
	_, _ = r.rdb.Set(ctx, key, payload, ttl).Result()
}

func (r *Redis) Del(ctx context.Context, key string) {
	if r == nil || r.rdb == nil {
		return
	}
	_, _ = r.rdb.Del(ctx, key).Result()
}

// DelPrefix SCANs for shopkeet:cache:{prefix}* and UNLINKS them. Bounded by
// cache volume, not key length; see cache_test for shape expectations.
func (r *Redis) DelPrefix(ctx context.Context, prefix string) {
	if r == nil || r.rdb == nil || prefix == "" {
		return
	}
	match := "shopkeet:cache:" + prefix + "*"
	iter := r.rdb.Scan(ctx, 0, match, 64).Iterator()
	var keys []string
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
		if len(keys) >= 256 {
			_, _ = r.rdb.Unlink(ctx, keys...).Result()
			keys = keys[:0]
		}
	}
	if err := iter.Err(); err == nil && len(keys) > 0 {
		_, _ = r.rdb.Unlink(ctx, keys...).Result()
	}
}

func (r *Redis) Close() error {
	if r == nil || r.rdb == nil {
		return nil
	}
	return r.rdb.Close()
}
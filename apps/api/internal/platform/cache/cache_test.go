package cache

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestProductKeyNamespacing locks the "shopkeet:cache:product:" convention so
// catalog and orders agree on the exact keys they read/invalidate.
func TestProductKeyNamespacing(t *testing.T) {
	k := ProductKey("tenant-1", "prod-42", "public")
	if want := "shopkeet:cache:product:tenant-1/prod-42/public"; k != want {
		t.Fatalf("ProductKey = %q, want %q", k, want)
	}
	if k == ProductKey("tenant-1", "prod-42", "admin") {
		t.Fatal("public and admin viewers must not share a key")
	}
}

// TestNoopIsAReadFallback confirms the disabled-cache path never returns a hit
// and never panics (services default to cache.Noop{}).
func TestNoopIsAReadFallback(t *testing.T) {
	ctx := context.Background()
	var c Cache = Noop{}
	if _, ok := c.Get(ctx, "any"); ok {
		t.Fatal("Noop cached read returned a hit")
	}
	c.Set(ctx, "any", []byte("x"), time.Minute)
	c.Del(ctx, "any")
	c.DelPrefix(ctx, "any")
	if err := c.Close(); err != nil {
		t.Fatalf("Noop.Close: %v", err)
	}
}

// NewTestRedis returns a Redis cache backed by REDIS_URL when set, else a live
// client from a raw URL for the integration tests below. Missing REDIS_URL
// skips (the tunnel is local dev plumbing, not a unit prerequisite).
func newTestRedis(t *testing.T) *Redis {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set; skipping cache integration")
	}
	c, err := NewRedis(url)
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// TestRedisSetGetRoundTrip exercises cache-aside set/get + TTL expiry against
// the shared Redis via REDIS_URL.
func TestRedisSetGetRoundTrip(t *testing.T) {
	c := newTestRedis(t)
	ctx := context.Background()
	key := ProductKey("cachetest-"+t.Name(), "p1", "public")
	payload := []byte(`{"id":"p1"}`)

	c.Set(ctx, key, payload, time.Minute)
	got, ok := c.Get(ctx, key)
	if !ok {
		t.Fatal("cache miss after Set")
	}
	if string(got) != string(payload) {
		t.Fatalf("got %q want %q", got, payload)
	}

	c.Del(ctx, key)
	if _, ok := c.Get(ctx, key); ok {
		t.Fatal("hit after Del")
	}

	// Expiry path: 1s TTL, verify it clears.
	c.Set(ctx, key, payload, time.Second)
	time.Sleep(1500 * time.Millisecond)
	if _, ok := c.Get(ctx, key); ok {
		t.Fatal("hit after TTL window")
	}
}

// TestRedisDelPrefix removes shopkeet:cache:{prefix}* keys without touching
// neighbors (this is how service code invalidates a tenant's entries).
func TestRedisDelPrefix(t *testing.T) {
	c := newTestRedis(t)
	ctx := context.Background()
	tid := "cachetest-" + t.Name()
	c.Set(ctx, ProductKey(tid, "p1", "public"), []byte("1"), time.Minute)
	c.Set(ctx, ProductKey(tid, "p2", "public"), []byte("1"), time.Minute)
	c.Set(ctx, "shopkeet:other:x", []byte("keep"), time.Minute)

	// Product keys live under scope "product:{tid}", so invalidate that prefix.
	c.DelPrefix(ctx, "product:"+tid)

	for _, id := range []string{"p1", "p2"} {
		if _, ok := c.Get(ctx, ProductKey(tid, id, "public")); ok {
			t.Fatalf("key %s survived DelPrefix", id)
		}
	}
	if _, ok := c.Get(ctx, "shopkeet:other:x"); !ok {
		t.Fatal("DelPrefix swept a key outside shopkeet:cache:{prefix}*")
	}
	// Cleanup stray.
	c.Del(ctx, "shopkeet:other:x")
}

// TestInvalidateProduct drops both viewer variants (the exact contract the
// catalog Service and checkout depend on).
func TestInvalidateProduct(t *testing.T) {
	c := newTestRedis(t)
	ctx := context.Background()
	tid := "cachetest-" + t.Name()
	pub := ProductKey(tid, "p9", "public")
	adm := ProductKey(tid, "p9", "admin")
	c.Set(ctx, pub, []byte("1"), time.Minute)
	c.Set(ctx, adm, []byte("1"), time.Minute)

	InvalidateProduct(ctx, c, tid, "p9")

	if _, ok := c.Get(ctx, pub); ok {
		t.Fatal("public entry survived InvalidateProduct")
	}
	if _, ok := c.Get(ctx, adm); ok {
		t.Fatal("admin entry survived InvalidateProduct")
	}
}
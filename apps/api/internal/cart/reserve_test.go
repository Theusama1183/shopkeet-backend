package cart

import (
	"context"
	"os"
	"testing"
	"time"
)

// TestRedisReserver is optional: it skips unless REDIS_URL is set (the
// real-time test needs a live Redis, e.g. the VPS one via SSH tunnel).
func TestRedisReserver(t *testing.T) {
	url := os.Getenv("REDIS_URL")
	if url == "" {
		t.Skip("REDIS_URL not set; skipping redis reservation test")
	}
	ctx := context.Background()
	r, err := NewRedisReserver(url)
	if err != nil {
		t.Fatalf("NewRedisReserver: %v", err)
	}
	defer r.Close()

	prod := "test-product-" + time.Now().Format("150405.000000000")
	if err := r.ReserveUnits(ctx, prod, 3); err != nil {
		t.Fatalf("ReserveUnits: %v", err)
	}
	// Redis is a fast path: it must never surface errors even for junk input.
	if err := r.ReserveUnits(ctx, prod, -1); err != nil {
		t.Fatalf("ReserveUnits(-1) should be a no-op: %v", err)
	}
	// Verify the keys exist with the 15-minute TTL.
	for i := 0; i < 3; i++ {
		val, err := r.rdb.Get(ctx, "shopkeet:reserve:"+prod+":"+string(rune('0'+i))).Result()
		if err != nil {
			t.Fatalf("key %d missing: %v", i, err)
		}
		if val == "" {
			t.Fatalf("key %d has empty value", i)
		}
		ttl, err := r.rdb.TTL(ctx, "shopkeet:reserve:"+prod+":"+string(rune('0'+i))).Result()
		if err != nil {
			t.Fatalf("TTL for key %d: %v", i, err)
		}
		if ttl <= 0 || ttl > ReserveTTL {
			t.Fatalf("key %d TTL %v outside (0, %v]", i, ttl, ReserveTTL)
		}
	}
}

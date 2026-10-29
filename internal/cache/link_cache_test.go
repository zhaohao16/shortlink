package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestCache(t *testing.T) (*LinkCache, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	return NewLinkCache(rdb, time.Hour), mr
}

func TestSetThenGet(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()
	if err := c.Set(ctx, "aB3c9F", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	url, found, err := c.Get(ctx, "aB3c9F")
	if err != nil || !found || url != "https://example.com" {
		t.Fatalf("got %q found=%v err=%v", url, found, err)
	}
}

func TestGetMiss(t *testing.T) {
	c, _ := newTestCache(t)
	_, found, err := c.Get(context.Background(), "zzzzzz")
	if err != nil || found {
		t.Fatalf("found=%v err=%v, want miss", found, err)
	}
}

func TestNegativeCache(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()
	if err := c.SetNegative(ctx, "qqqqqq"); err != nil {
		t.Fatal(err)
	}
	url, found, err := c.Get(ctx, "qqqqqq")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("negative cache should count as hit")
	}
	if url != "" {
		t.Fatalf("negative entry url = %q, want empty", url)
	}
}

func TestTTLJitterApplied(t *testing.T) {
	c, mr := newTestCache(t)
	if err := c.Set(context.Background(), "aB3c9F", "https://example.com"); err != nil {
		t.Fatal(err)
	}
	// 抖动范围是 ±10%，因此上限是 1.1 × TTL
	maxTTL := time.Hour + time.Hour/10
	ttl := mr.TTL("link:aB3c9F")
	if ttl <= 0 || ttl > maxTTL {
		t.Fatalf("ttl = %v, want 0 < ttl <= %v", ttl, maxTTL)
	}
}

func TestJitterVaries(t *testing.T) {
	c, _ := newTestCache(t)
	seen := make(map[time.Duration]struct{})
	for i := 0; i < 50; i++ {
		seen[c.jitter()] = struct{}{}
	}
	if len(seen) < 2 {
		t.Fatal("jitter produced identical TTLs, want variation")
	}
}

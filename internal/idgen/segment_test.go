package idgen

import (
	"context"
	"sync"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestGen(t *testing.T, step uint64, start uint64) (*Generator, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := New(context.Background(), rdb, "test:idgen", step, start)
	if err != nil {
		t.Fatalf("new generator: %v", err)
	}
	return g, mr
}

func TestNextIsMonotonicAndUnique(t *testing.T) {
	g, _ := newTestGen(t, 10, 0)
	ctx := context.Background()
	var prev uint64
	for i := 0; i < 35; i++ {
		v, err := g.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if v <= prev {
			t.Fatalf("not monotonic: %d after %d", v, prev)
		}
		prev = v
	}
}

// TestSegmentAllocatedOncePerBatch 证明 100 次取号只向 Redis 申请了一次号段。
func TestSegmentAllocatedOncePerBatch(t *testing.T) {
	g, mr := newTestGen(t, 100, 0)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if _, err := g.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}
	got, err := mr.Get("test:idgen")
	if err != nil {
		t.Fatal(err)
	}
	if got != "100" {
		t.Fatalf("redis counter = %s, want 100", got)
	}
}

func TestConcurrentNextNoDuplicate(t *testing.T) {
	g, _ := newTestGen(t, 100, 0)
	ctx := context.Background()
	var mu sync.Mutex
	seen := make(map[uint64]struct{}, 5000)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				v, err := g.Next(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if _, dup := seen[v]; dup {
					t.Errorf("duplicate seq %d", v)
				}
				seen[v] = struct{}{}
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(seen) != 5000 {
		t.Fatalf("issued %d, want 5000", len(seen))
	}
}

// TestRestartDoesNotRenumber 模拟"Redis 里已有更大计数"的重启场景：
// 新进程的 start 更小，不得把计数改小，否则会重发已用过的号。
func TestRestartDoesNotRenumber(t *testing.T) {
	_, mr := newTestGen(t, 10, 0)
	mr.Set("test:idgen", "500")
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := New(context.Background(), rdb, "test:idgen", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	v, err := g.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v <= 500 {
		t.Fatalf("issued %d, want > 500", v)
	}
}

func TestStartValueAppliedOnEmptyRedis(t *testing.T) {
	g, _ := newTestGen(t, 10, 100000)
	v, err := g.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != 100000 {
		t.Fatalf("first issued = %d, want exactly 100000", v)
	}
}

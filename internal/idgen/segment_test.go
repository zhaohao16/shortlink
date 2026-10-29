package idgen

import (
	"context"
	"sync"
	"testing"
	"time"

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

// TestCounterBumpedWhenBehindDB 模拟 Redis 从陈旧 AOF 恢复：计数器落后于库里已用的 MAX(seq)，
// 启动时必须被抬到 start-1，否则会重发已用过的号并撞 uk_seq。
func TestCounterBumpedWhenBehindDB(t *testing.T) {
	mr := miniredis.RunT(t)
	mr.Set("stale:idgen", "5")
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})

	g, err := New(context.Background(), rdb, "stale:idgen", 10, 100)
	if err != nil {
		t.Fatal(err)
	}
	v, err := g.Next(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if v != 100 {
		t.Fatalf("first issued = %d, want 100 (counter must be lifted to start-1)", v)
	}
	if got, _ := mr.Get("stale:idgen"); got != "109" {
		t.Fatalf("counter = %s, want 109", got)
	}
}

func TestStepTooLargeRejected(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	if _, err := New(context.Background(), rdb, "k", MaxStep+1, 1); err == nil {
		t.Fatal("want error for oversized step")
	}
}

// TestNextFailsFastUnderConcurrency 是 Blocking 级回归：
// Redis 不可达且号段为空时，并发取号的总耗时必须接近"一次超时"，
// 而不是"并发数 × 超时"（旧实现持锁做网络 I/O，32 并发能拖到 10s 以上）。
func TestNextFailsFastUnderConcurrency(t *testing.T) {
	// 192.0.2.0/24 是 RFC 5737 的 TEST-NET-1，不可路由，用来制造真实超时
	rdb := redis.NewClient(&redis.Options{
		Addr:         "192.0.2.1:6379",
		DialTimeout:  300 * time.Millisecond,
		ReadTimeout:  300 * time.Millisecond,
		WriteTimeout: 300 * time.Millisecond,
		MaxRetries:   -1,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// 绕过 New：New 需要 Redis 可达才能初始化，本用例要测的是 Next 的失败路径。
	g := &Generator{step: 10, key: "dead:idgen", rdb: rdb}

	const n = 20
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := g.Next(ctx); err == nil {
				t.Error("want error when redis is unreachable")
			}
		}()
	}
	wg.Wait()

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("%d concurrent Next took %v, want ~one timeout (fail-fast broken)", n, elapsed)
	}
}

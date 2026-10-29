package idgen

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	// DefaultStep 是默认号段大小。
	DefaultStep = 1000
	// MaxStep 限制号段大小，避免 int64 转换溢出或一次浪费过多号。
	MaxStep = 1 << 20
	// fetchTimeout 是向 Redis 申请新号段的时间上限。
	fetchTimeout = 500 * time.Millisecond
	// failCooldown 是取号失败后的冷却期：期内不再访问 Redis，直接失败。
	// 没有它，Redis 故障时每个并发请求都会各自等一轮超时，总耗时为 N × 超时。
	failCooldown = time.Second
)

// ensureScript 把计数器抬到不低于 ARGV[1]，且永不回退。
// 用于 Redis 从陈旧 AOF 恢复后，避免计数器落后于库里已用的 MAX(seq) 而撞号。
const ensureScript = `
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local floor = tonumber(ARGV[1])
if cur < floor then
  redis.call('SET', KEYS[1], floor)
  return floor
end
return cur
`

// Generator 号段发号器：每次向 Redis 申请 step 个号，进程内消费完再申请。
// 进程重启会丢弃当前号段剩余部分，只浪费号，不会重发已用过的号。
type Generator struct {
	mu  sync.Mutex // 保护 cur/max
	cur uint64     // 已发出的最后一个号
	max uint64     // 当前号段上界

	fetchMu    sync.Mutex // 串行化号段申请，避免并发穿透到 Redis
	lastErr    error
	lastFailAt time.Time

	step uint64
	key  string
	rdb  redis.Cmdable
}

// New 初始化发号器。start 是期望的最小起点（通常为库中 MAX(seq)+1）：
// 计数器只会被抬到 start-1 以上，绝不回退。
func New(ctx context.Context, rdb redis.Cmdable, key string, step, start uint64) (*Generator, error) {
	if step == 0 {
		step = DefaultStep
	}
	if step > MaxStep {
		return nil, fmt.Errorf("idgen: step %d exceeds max %d", step, MaxStep)
	}
	if start == 0 {
		start = 1
	}
	// 计数器语义是"已分配到的最大号"，故下限取 start-1。
	if err := rdb.Eval(ctx, ensureScript, []string{key}, start-1).Err(); err != nil {
		return nil, fmt.Errorf("idgen: init counter: %w", err)
	}
	return &Generator{step: step, key: key, rdb: rdb}, nil
}

// Next 返回全局唯一、单调递增的序列号。
func (g *Generator) Next(ctx context.Context) (uint64, error) {
	if v, ok := g.take(); ok {
		return v, nil
	}

	// 号段耗尽：只有这里会访问 Redis。串行化 + 双检查 + 失败冷却，
	// 保证 Redis 故障时总耗时是一次超时，而不是并发数 × 超时。
	g.fetchMu.Lock()
	defer g.fetchMu.Unlock()

	if v, ok := g.take(); ok { // 等锁期间可能已有人补过号段
		return v, nil
	}
	if g.lastErr != nil && time.Since(g.lastFailAt) < failCooldown {
		return 0, fmt.Errorf("idgen: fetch segment: %w", g.lastErr)
	}

	fetchCtx, cancel := context.WithTimeout(ctx, fetchTimeout)
	end, err := g.rdb.IncrBy(fetchCtx, g.key, int64(g.step)).Result()
	cancel()
	if err != nil {
		g.lastErr, g.lastFailAt = err, time.Now()
		return 0, fmt.Errorf("idgen: fetch segment: %w", err)
	}
	if end < int64(g.step) { // 只有溢出/负数才会成立
		return 0, fmt.Errorf("idgen: invalid counter %d", end)
	}

	g.mu.Lock()
	g.max = uint64(end)
	g.cur = g.max - g.step
	g.mu.Unlock()

	v, ok := g.take()
	if !ok {
		return 0, fmt.Errorf("idgen: segment exhausted immediately")
	}
	return v, nil
}

func (g *Generator) take() (uint64, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cur >= g.max {
		return 0, false
	}
	g.cur++
	return g.cur, true
}

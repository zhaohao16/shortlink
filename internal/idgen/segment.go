package idgen

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

// DefaultStep 是默认号段大小。
const DefaultStep = 1000

// Generator 号段发号器：每次向 Redis 申请 step 个号，进程内消费完再申请。
// 进程重启会丢弃当前号段剩余部分，只浪费号，不会重发已用过的号。
type Generator struct {
	mu   sync.Mutex
	cur  uint64 // 已发出的最后一个号
	max  uint64 // 当前号段上界
	step uint64
	key  string
	rdb  redis.Cmdable
}

// New 初始化发号器。start 仅在 Redis 键不存在时生效，
// 用于 Redis 数据丢失后从库里 MAX(seq) 继续，避免从 0 重发导致撞号。
func New(ctx context.Context, rdb redis.Cmdable, key string, step, start uint64) (*Generator, error) {
	if step == 0 {
		step = DefaultStep
	}
	if start == 0 {
		start = 1
	}
	// 计数器语义是"已分配到的最大号"，故初值取 start-1，
	// SetNX 保证键已存在时保留原值、绝不回退计数。
	if err := rdb.SetNX(ctx, key, start-1, 0).Err(); err != nil {
		return nil, fmt.Errorf("idgen: init counter: %w", err)
	}
	return &Generator{step: step, key: key, rdb: rdb}, nil
}

// Next 返回全局唯一、单调递增的序列号。
func (g *Generator) Next(ctx context.Context) (uint64, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.cur >= g.max {
		end, err := g.rdb.IncrBy(ctx, g.key, int64(g.step)).Result()
		if err != nil {
			return 0, fmt.Errorf("idgen: fetch segment: %w", err)
		}
		if end <= 0 {
			return 0, fmt.Errorf("idgen: invalid counter %d", end)
		}
		g.max = uint64(end)
		g.cur = g.max - g.step
	}
	g.cur++
	return g.cur, nil
}

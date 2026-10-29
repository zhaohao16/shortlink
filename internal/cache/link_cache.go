package cache

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
)

// emptyMarker 是空值缓存的值。真实 URL 必以 http 开头，故空串可无歧义地表示"不存在"。
const emptyMarker = ""

// negativeTTL 是空值缓存寿命，短于正常缓存，避免短码被提前创建后长时间 404。
const negativeTTL = 60 * time.Second

// LinkCache 封装短码到原始 URL 的缓存读写。
type LinkCache struct {
	rdb redis.Cmdable
	ttl time.Duration
}

func NewLinkCache(rdb redis.Cmdable, ttl time.Duration) *LinkCache {
	return &LinkCache{rdb: rdb, ttl: ttl}
}

func cacheKey(code string) string { return "link:" + code }

// jitter 给 TTL 加 ±10% 抖动，避免同批写入的键同时过期造成缓存雪崩。
func (c *LinkCache) jitter() time.Duration {
	d := time.Duration(float64(c.ttl) * (0.9 + rand.Float64()*0.2))
	if d <= 0 {
		return time.Second
	}
	return d
}

// Set 写入短码到 URL 的映射。
func (c *LinkCache) Set(ctx context.Context, code, url string) error {
	return c.rdb.Set(ctx, cacheKey(code), url, c.jitter()).Err()
}

// SetNegative 写入空值缓存，防穿透。
func (c *LinkCache) SetNegative(ctx context.Context, code string) error {
	return c.rdb.Set(ctx, cacheKey(code), emptyMarker, negativeTTL).Err()
}

// Get 返回 (url, found, err)。命中空值缓存时返回 ("", true, nil)。
func (c *LinkCache) Get(ctx context.Context, code string) (string, bool, error) {
	v, err := c.rdb.Get(ctx, cacheKey(code)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}

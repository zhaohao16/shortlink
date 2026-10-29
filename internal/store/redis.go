package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	redisPingAttempts = 15
	redisPingInterval = time.Second
)

// OpenRedis 建立客户端，并在启动期做有限次重试。
func OpenRedis(ctx context.Context, addr string) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		PoolSize:     100,
	})

	var lastErr error
	for i := 0; i < redisPingAttempts; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = rdb.Ping(pingCtx).Err()
		cancel()
		if lastErr == nil {
			return rdb, nil
		}
		select {
		case <-ctx.Done():
			_ = rdb.Close()
			return nil, ctx.Err()
		case <-time.After(redisPingInterval):
		}
	}
	_ = rdb.Close()
	return nil, fmt.Errorf("redis not reachable at %s: %w", addr, lastErr)
}

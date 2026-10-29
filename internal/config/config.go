package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config 汇总运行期全部可调参数，全部来自环境变量。
type Config struct {
	HTTPAddr    string
	BaseURL     string
	DBDSN       string
	RedisAddr   string
	CacheTTL    time.Duration
	SegmentSize uint64
	SeqStart    uint64
}

// Load 读取环境变量并做必填校验。缺必填项返回 error，由 main 打印后退出。
func Load() (*Config, error) {
	c := &Config{
		HTTPAddr:    env("HTTP_ADDR", ":8080"),
		BaseURL:     env("BASE_URL", "http://localhost:8080"),
		DBDSN:       env("DB_DSN", ""),
		RedisAddr:   env("REDIS_ADDR", "localhost:6379"),
		CacheTTL:    time.Duration(envInt("CACHE_TTL_SECONDS", 86400)) * time.Second,
		SegmentSize: uint64(envInt("SEGMENT_SIZE", 1000)),
		SeqStart:    uint64(envInt("SEQ_START", 100000)),
	}
	if c.DBDSN == "" {
		return nil, fmt.Errorf("config: DB_DSN is required")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

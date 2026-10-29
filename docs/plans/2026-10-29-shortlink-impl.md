# 短链接系统 实施计划

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** 用 Go(Gin)+MySQL+Redis 实现「创建短链 / 访问跳转」最小闭环，单二进制内嵌前端，docker compose 一键启动。

**Architecture:** 号段发号器（Redis `INCRBY`）产出单调递增 `seq`，经 32 位 Feistel 双射打散后 Base62 编码得定长 6 位短码；MySQL 唯一键兜底唯一性，Redis 缓存承接读流量。

**Tech Stack:** Go 1.24、gin v1.10、go-sql-driver/mysql v1.8、go-redis/v9、miniredis（测试）、MySQL 8、Redis 7、Docker Compose

**前置约定：** 所有命令在仓库根目录 `shortlink/` 执行；`GOOS` 为宿主平台，容器内为 linux/amd64。

---

## 任务总览

| # | 任务 | 关键产出 | 依赖 |
|---|---|---|---|
| T1 | 项目骨架与配置 | `go.mod`、`internal/config` | — |
| T2 | Base62 编解码 | `internal/shortcode/base62.go` | T1 |
| T3 | Feistel 双射打散 | `internal/shortcode/feistel.go` | T2 |
| T4 | 号段发号器 | `internal/idgen/segment.go` | T1 |
| T5 | MySQL 连接与自动迁移 | `internal/store/mysql.go` | T1 |
| T6 | Redis 连接与链接缓存 | `internal/store/redis.go`、`internal/cache` | T5 |
| T7 | 仓储层与创建链路 | `internal/repo`、`internal/service` | T3/T4/T5/T6 |
| T8 | 创建接口 | `internal/httpapi` | T7 |
| T9 | 跳转接口（NoRoute 兜底） | `internal/httpapi` | T7 |
| T10 | 内嵌前端页面 | `web/index.html` | T1 |
| T11 | 健康检查与中间件 | `internal/httpapi` | T5/T6 |
| T12 | 容器化与编排 | `Dockerfile`、`docker-compose.yml` | T8/T9/T10/T11 |
| T13 | 端到端验收 | `scripts/verify.sh`、`README.md` | T12 |

---

## T1 项目骨架与配置

**产出文件**
- Create: `go.mod`、`cmd/server/main.go`（临时桩）、`internal/config/config.go`、`.gitignore`、`.dockerignore`

**步骤**

1. 初始化模块并拉依赖（固定大版本，不自选小版本）：

```bash
go mod init shortlink
go get github.com/gin-gonic/gin@v1.10.0
go get github.com/go-sql-driver/mysql@v1.8.1
go get github.com/redis/go-redis/v9@v9.7.0
go get github.com/alicebob/miniredis/v2@v2.33.0
```

2. 写 `internal/config/config.go`：从环境变量解析，集中默认值，缺失必填项返回 error。

```go
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr    string
	BaseURL     string
	DBDSN       string
	RedisAddr   string
	CacheTTL    time.Duration
	SegmentSize uint64
	SeqStart    uint64
}

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
```

3. `cmd/server/main.go` 先放能编译的桩，只打印配置：

```go
package main

import (
	"log/slog"
	"shortlink/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	slog.Info("boot", "addr", cfg.HTTPAddr, "base_url", cfg.BaseURL)
}
```

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V1.1 | `go build ./...` | 退出码 0，无输出 |
| V1.2 | `go vet ./...` | 退出码 0 |
| V1.3 | 不设 `DB_DSN` 运行 `go run ./cmd/server` | 输出 `load config` + `DB_DSN is required`，退出码 1 |
| V1.4 | `DB_DSN=x go run ./cmd/server` | 输出 `boot addr=:8080 base_url=http://localhost:8080` |

```bash
git add -A && git commit -m "chore: 初始化项目骨架与配置加载"
```

---

## T2 Base62 编解码

**产出文件**
- Create: `internal/shortcode/base62.go`、`internal/shortcode/base62_test.go`

**步骤**

1. 先写失败测试 `base62_test.go`：

```go
package shortcode

import "testing"

func TestBase62RoundTrip(t *testing.T) {
	cases := []uint64{0, 1, 61, 62, 12345, 56800235583}
	for _, n := range cases {
		s := encodeBase62(n)
		got, err := decodeBase62(s)
		if err != nil {
			t.Fatalf("decode %q: %v", s, err)
		}
		if got != n {
			t.Fatalf("round trip %d -> %q -> %d", n, s, got)
		}
	}
}

func TestBase62AlphabetOnly(t *testing.T) {
	for n := uint64(0); n < 10000; n++ {
		for _, r := range encodeBase62(n) {
			ok := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			if !ok {
				t.Fatalf("illegal char %q in encoding of %d", r, n)
			}
		}
	}
}
```

2. 运行测试确认编译失败：`go test ./internal/shortcode/ -run TestBase62 -v` → `undefined: encodeBase62`。

3. 实现：

```go
package shortcode

import "fmt"

const alphabet = "0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

var alphabetIndex [256]int16

func init() {
	for i := range alphabetIndex {
		alphabetIndex[i] = -1
	}
	for i := 0; i < len(alphabet); i++ {
		alphabetIndex[alphabet[i]] = int16(i)
	}
}

func encodeBase62(n uint64) string {
	if n == 0 {
		return "0"
	}
	var buf [11]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = alphabet[n%62]
		n /= 62
	}
	return string(buf[i:])
}

func decodeBase62(s string) (uint64, error) {
	if s == "" {
		return 0, fmt.Errorf("shortcode: empty string")
	}
	var n uint64
	for i := 0; i < len(s); i++ {
		d := alphabetIndex[s[i]]
		if d < 0 {
			return 0, fmt.Errorf("shortcode: invalid character %q", s[i])
		}
		n = n*62 + uint64(d)
	}
	return n, nil
}
```

4. 运行：`go test ./internal/shortcode/ -run TestBase62 -v` → 两条 `--- PASS`。

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V2.1 | 步骤 2 的 `go test` | 编译失败，提示 `undefined: encodeBase62`（证明测试先于实现） |
| V2.2 | 步骤 4 的 `go test -v` | `TestBase62RoundTrip PASS`、`TestBase62AlphabetOnly PASS` |
| V2.3 | `go test ./internal/shortcode/ -race -count=1` | PASS，无 data race |

```bash
git add internal/shortcode && git commit -m "feat(shortcode): 添加 base62 编解码"
```

---

## T3 Feistel 双射打散与短码生成

**产出文件**
- Create: `internal/shortcode/feistel.go`、`internal/shortcode/code.go`、`internal/shortcode/code_test.go`

**步骤**

1. 写失败测试（核心是「双射」性质与「恒 6 位」）：

```go
package shortcode

import "testing"

func TestMix32IsBijective(t *testing.T) {
	const n = 200000
	seen := make(map[uint32]struct{}, n)
	for x := uint32(0); x < n; x++ {
		y := Mix32(x)
		if _, dup := seen[y]; dup {
			t.Fatalf("collision: Mix32(%d) reused", x)
		}
		seen[y] = struct{}{}
	}
}

func TestMix32RoundTrip(t *testing.T) {
	for _, x := range []uint32{0, 1, 100000, 1 << 31, ^uint32(0)} {
		if got := Unmix32(Mix32(x)); got != x {
			t.Fatalf("unmix(mix(%d)) = %d", x, got)
		}
	}
}

func TestCodeIsAlwaysSixChars(t *testing.T) {
	for seq := uint64(100000); seq < 100000+50000; seq++ {
		code, err := Encode(seq)
		if err != nil {
			t.Fatalf("encode %d: %v", seq, err)
		}
		if len(code) != 6 {
			t.Fatalf("encode %d = %q, want len 6", seq, code)
		}
	}
}

func TestCodeUnique(t *testing.T) {
	seen := make(map[string]uint64)
	for seq := uint64(100000); seq < 100000+200000; seq++ {
		code, _ := Encode(seq)
		if prev, dup := seen[code]; dup {
			t.Fatalf("code %q collision between seq %d and %d", code, prev, seq)
		}
		seen[code] = seq
	}
}

func TestCodeSnapshot(t *testing.T) {
	// 固定快照：算法被误改时立即失败
	want := map[uint64]string{
		100000: "PLACEHOLDER_1",
		100001: "PLACEHOLDER_2",
		200000: "PLACEHOLDER_3",
	}
	for seq, w := range want {
		if got, _ := Encode(seq); got != w {
			t.Fatalf("snapshot seq %d = %q, want %q", seq, got, w)
		}
	}
}

func TestEncodeRejectsOverflow(t *testing.T) {
	if _, err := Encode(1 << 32); err == nil {
		t.Fatal("want error for seq >= 2^32")
	}
}

func TestIsValidCode(t *testing.T) {
	good := []string{"aB3c9F", "000000", "zzzzzz"}
	bad := []string{"", "abcde", "abcdefg", "aB3c9-", "aB3c9!", "../api"}
	for _, s := range good {
		if !IsValidCode(s) {
			t.Fatalf("%q should be valid", s)
		}
	}
	for _, s := range bad {
		if IsValidCode(s) {
			t.Fatalf("%q should be invalid", s)
		}
	}
}
```

2. 运行确认失败（`undefined: Mix32`）。

3. 实现 `feistel.go`：

```go
package shortcode

// 4 轮 16 位半块 Feistel 网络。Feistel 结构保证对 uint32 是双射，
// 因此不同 seq 必得不同输出，创建短链无需查重。
var roundKeys = [4]uint16{0x1B2C, 0x5D7E, 0x9A31, 0xC4F8}

func roundF(v, k uint16) uint16 {
	h := uint32(v)*0x9E37 + uint32(k)
	h ^= h >> 7
	h *= 0x85EB
	h ^= h >> 13
	h *= 0xC2B2
	h ^= h >> 16
	return uint16(h)
}

// Mix32 打散：把相邻 seq 映射到输出空间相距很远的位置，使短码不可枚举。
func Mix32(x uint32) uint32 {
	l, r := uint16(x>>16), uint16(x)
	for i := 0; i < 4; i++ {
		l, r = r, l^roundF(r, roundKeys[i])
	}
	return uint32(l)<<16 | uint32(r)
}

// Unmix32 是 Mix32 的逆，本期链路不依赖它，保留用于排障与反解 seq。
func Unmix32(y uint32) uint32 {
	l, r := uint16(y>>16), uint16(y)
	for i := 3; i >= 0; i-- {
		l, r = r^roundF(l, roundKeys[i]), l
	}
	return uint32(l)<<16 | uint32(r)
}
```

4. 实现 `code.go`：

```go
package shortcode

import (
	"errors"
	"math"
)

const (
	// codeMod = 62^6，短码空间大小
	codeMod = uint64(62 * 62 * 62 * 62 * 62 * 62)
	// codeOffset 把输出推高到 62^5 之上，保证恒为 6 位
	codeOffset = uint64(1) << 32
	CodeLen    = 6
)

var ErrSeqOverflow = errors.New("shortcode: seq exceeds 32-bit capacity")

// Encode 把单调自增 seq 转成定长 6 位短码。
// 全程为双射运算（Feistel 双射 + 模加偏移），因此输出唯一。
func Encode(seq uint64) (string, error) {
	if seq > math.MaxUint32 {
		return "", ErrSeqOverflow
	}
	y := (uint64(Mix32(uint32(seq))) + codeOffset) % codeMod
	return encodeBase62(y), nil
}

// Decode 反解短码为原始 seq，仅用于排障。
func Decode(code string) (uint64, error) {
	n, err := decodeBase62(code)
	if err != nil {
		return 0, err
	}
	if n < codeOffset {
		return 0, errors.New("shortcode: code out of range")
	}
	return uint64(Unmix32(uint32(n - codeOffset))), nil
}

func IsValidCode(s string) bool {
	if len(s) != CodeLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if alphabetIndex[s[i]] < 0 {
			return false
		}
	}
	return true
}
```

5. 运行 `go test ./internal/shortcode/ -v`。`TestCodeSnapshot` 必然失败（占位符）——把实际输出填回去：

```bash
go test ./internal/shortcode/ -run TestCodeSnapshot -v
# 失败信息里会打印 got 值，把 PLACEHOLDER_* 替换为真实短码，再跑一次
```

6. 追加 `TestDecodeRoundTrip`：`Decode(Encode(seq)) == seq`，覆盖 0/100000/2^32-1。

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V3.1 | 步骤 2 | 编译失败 `undefined: Mix32` |
| V3.2 | `TestMix32IsBijective` 20 万样本 | PASS，无 collision（双射成立） |
| V3.3 | `TestMix32RoundTrip` | PASS（可逆） |
| V3.4 | `TestCodeIsAlwaysSixChars` | PASS，5 万个 seq 全为 6 位 |
| V3.5 | `TestCodeUnique` 20 万样本 | PASS，无重复短码 |
| V3.6 | 打散效果人工抽查 `TestCodeSnapshot` 的三个值 | 6 位且互不相似、相邻 seq 结果无规律（不是 `000001/000002`） |
| V3.7 | `TestEncodeRejectsOverflow` | PASS，返回 `ErrSeqOverflow` |
| V3.8 | `go test ./internal/shortcode/ -race -count=1` | 全 PASS |

```bash
git add internal/shortcode && git commit -m "feat(shortcode): 引入 Feistel 双射打散，生成定长 6 位短码"
```

---

## T4 号段发号器

**产出文件**
- Create: `internal/idgen/segment.go`、`internal/idgen/segment_test.go`

**步骤**

1. 写失败测试，用 `miniredis` 替代真实 Redis：

```go
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

func TestSegmentAllocatedOncePerBatch(t *testing.T) {
	g, mr := newTestGen(t, 100, 0)
	ctx := context.Background()
	for i := 0; i < 100; i++ {
		if _, err := g.Next(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if got := mr.Get("test:idgen"); got != "100" {
		t.Fatalf("redis counter = %s, want 100 (只应申请一次号段)", got)
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

func TestRestartDoesNotRenumber(t *testing.T) {
	_, mr := newTestGen(t, 10, 0)
	mr.Set("test:idgen", "500") // 模拟 Redis 中已有计数
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := New(context.Background(), rdb, "test:idgen", 10, 100) // start 更小，不应覆盖
	if err != nil {
		t.Fatal(err)
	}
	v, _ := g.Next(context.Background())
	if v <= 500 {
		t.Fatalf("issued %d, want > 500", v)
	}
}
```

2. 运行确认失败。

3. 实现：

```go
package idgen

import (
	"context"
	"fmt"
	"sync"

	"github.com/redis/go-redis/v9"
)

// Generator 号段发号器：每次向 Redis 申请 step 个号，进程内消费完再申请。
type Generator struct {
	mu    sync.Mutex
	cur   uint64 // 已发出的最后一个号
	max   uint64 // 当前号段上界
	step  uint64
	key   string
	rdb   redis.Cmdable
}

// New 初始化发号器。start 仅在 Redis 键不存在时生效（防止 Redis 清空后从 0 重发导致撞号）。
func New(ctx context.Context, rdb redis.Cmdable, key string, step, start uint64) (*Generator, error) {
	if step == 0 {
		step = 1000
	}
	if start == 0 {
		start = 1
	}
	ok, err := rdb.SetNX(ctx, key, start, 0).Result()
	if err != nil {
		return nil, fmt.Errorf("idgen: init counter: %w", err)
	}
	_ = ok // 键已存在则保留原值，避免回退
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
```

4. 运行 `go test ./internal/idgen/ -race -v` → 4 条 PASS。

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V4.1 | `go test ./internal/idgen/ -race -v` | 4 条全 PASS |
| V4.2 | `TestSegmentAllocatedOncePerBatch` | 100 次取号后 Redis 计数恰为 `100`（号段复用生效） |
| V4.3 | `TestConcurrentNextNoDuplicate` | 50 goroutine × 100 次 = 5000 个不重复号 |
| V4.4 | `TestRestartDoesNotRenumber` | 重启后发出的号 > 已用计数（不清零、不回退） |

```bash
git add internal/idgen && git commit -m "feat(idgen): 实现 Redis 号段预分配发号器"
```

---

## T5 MySQL 连接与自动迁移

**产出文件**
- Create: `migrations/0001_init.sql`、`internal/store/mysql.go`

**步骤**

1. `migrations/0001_init.sql`（幂等，供 `go:embed` 执行）：

```sql
CREATE TABLE IF NOT EXISTS t_link (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    seq        BIGINT UNSIGNED NOT NULL COMMENT '发号器序列',
    code       VARCHAR(16)     NOT NULL COMMENT '6 位 base62 短码',
    url        VARCHAR(2048)   NOT NULL COMMENT '原始长链接',
    created_at TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_code (code),
    UNIQUE KEY uk_seq (seq)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;
```

2. `internal/store/mysql.go`：连接池参数 + 启动重试 + 执行迁移 + `MaxSeq` 查询。

```go
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

//go:embed all:migrations
var migrationFS embed.FS

func OpenMySQL(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	var lastErr error
	for i := 0; i < 15; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return db, nil
		}
		slog.Warn("mysql not ready, retrying", "attempt", i+1, "err", lastErr)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, fmt.Errorf("mysql not reachable: %w", lastErr)
}

func Migrate(ctx context.Context, db *sql.DB) error {
	body, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply migration: %w", err)
	}
	return nil
}

func MaxSeq(ctx context.Context, db *sql.DB) (uint64, error) {
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT COALESCE(MAX(seq), 0) FROM t_link").Scan(&v); err != nil {
		return 0, fmt.Errorf("query max seq: %w", err)
	}
	if !v.Valid || v.Int64 < 0 {
		return 0, nil
	}
	return uint64(v.Int64), nil
}
```

> 注意：`embed` 只能嵌入**本包目录下**的文件。因此把 `migrations/0001_init.sql` 放到 `internal/store/migrations/0001_init.sql`，`//go:embed all:migrations` 才成立。根目录那份仅供人工查阅时用符号链接或删除——**以 `internal/store/migrations/` 为唯一来源**。

3. 用真实 MySQL 验证（本机 docker 起一个临时实例）：

```bash
docker run -d --name sl-mysql-test -e MYSQL_ROOT_PASSWORD=root -e MYSQL_DATABASE=shortlink -p 3307:3306 mysql:8.0
# 等 ~20s 就绪后：
DB_DSN='root:root@tcp(127.0.0.1:3307)/shortlink?parseTime=true&charset=utf8mb4' go run ./cmd/server
```

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V5.1 | `go build ./...` | 0 退出码（embed 路径正确） |
| V5.2 | 首次启动后 `docker exec sl-mysql-test mysql -uroot -proot shortlink -e "SHOW CREATE TABLE t_link\G"` | 表存在，含 `uk_code`、`uk_seq` 两个唯一键 |
| V5.3 | 连续启动两次应用 | 第二次不报错（迁移幂等） |
| V5.4 | 插入重复短码 `INSERT INTO t_link(seq,code,url) VALUES(1,'same','http://a'),(2,'same','http://b');` | 报 `ERROR 1062 Duplicate entry` |
| V5.5 | 停掉 MySQL 后启动应用 | 重试 15 次后报 `mysql not reachable` 并退出，不挂死 |

```bash
git add internal/store migrations && git commit -m "feat(store): MySQL 连接、自动迁移与序号上界查询"
```

---

## T6 Redis 连接与链接缓存

**产出文件**
- Create: `internal/store/redis.go`、`internal/cache/link_cache.go`、`internal/cache/link_cache_test.go`

**步骤**

1. 写失败测试（覆盖命中、穿透、空值标记）：

```go
package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func newTestCache(t *testing.T) (*LinkCache, *miniredis.Miniredis) {
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
	_, found, err := c.Get(ctx, "qqqqqq")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("negative cache should count as hit")
	}
}

func TestTTLJitterApplied(t *testing.T) {
	c, mr := newTestCache(t)
	_ = c.Set(context.Background(), "aB3c9F", "https://example.com")
	ttl := mr.TTL("link:aB3c9F")
	if ttl <= 0 || ttl > time.Hour {
		t.Fatalf("ttl = %v, want 0 < ttl <= 1h", ttl)
	}
}
```

2. 运行确认失败。

3. `internal/store/redis.go`：

```go
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

func OpenRedis(ctx context.Context, addr string) (*redis.Client, error) {
	rdb := redis.NewClient(&redis.Options{
		Addr:         addr,
		DialTimeout:  3 * time.Second,
		ReadTimeout:  time.Second,
		WriteTimeout: time.Second,
		PoolSize:     100,
	})
	var lastErr error
	for i := 0; i < 15; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = rdb.Ping(pingCtx).Err()
		cancel()
		if lastErr == nil {
			return rdb, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil, fmt.Errorf("redis not reachable at %s: %w", addr, lastErr)
}
```

4. `internal/cache/link_cache.go`：值用前缀区分「空值标记」，TTL 带 ±10% 抖动。

```go
package cache

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/redis/go-redis/v9"
)

// emptyMarker 作为空值缓存的值，与真实 URL 区分（真实 URL 必以 http 开头）。
const emptyMarker = ""

type LinkCache struct {
	rdb redis.Cmdable
	ttl time.Duration
}

func NewLinkCache(rdb redis.Cmdable, ttl time.Duration) *LinkCache {
	return &LinkCache{rdb: rdb, ttl: ttl}
}

func key(code string) string { return "link:" + code }

func (c *LinkCache) jitter() time.Duration {
	// ±10%，避免同批写入的键同时过期
	d := time.Duration(float64(c.ttl) * (0.9 + rand.Float64()*0.2))
	if d <= 0 {
		d = time.Second
	}
	return d
}

func (c *LinkCache) Set(ctx context.Context, code, url string) error {
	return c.rdb.Set(ctx, key(code), url, c.jitter()).Err()
}

// SetNegative 写入空值缓存，防穿透，TTL 固定 60s。
func (c *LinkCache) SetNegative(ctx context.Context, code string) error {
	return c.rdb.Set(ctx, key(code), emptyMarker, 60*time.Second).Err()
}

// Get 返回 (url, found, err)。命中空值缓存时返回 ("", true, nil)。
func (c *LinkCache) Get(ctx context.Context, code string) (string, bool, error) {
	v, err := c.rdb.Get(ctx, key(code)).Result()
	if err == redis.Nil {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return v, true, nil
}
```

5. 运行 `go test ./internal/cache/ -race -v` → 4 条 PASS。

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V6.1 | `go test ./internal/cache/ -race -v` | 4 条全 PASS |
| V6.2 | `TestNegativeCache` | 空值缓存被视为「命中」，调用方据此返回 404 而不查库 |
| V6.3 | `TestTTLJitterApplied` | TTL 落在 (0, 1h]，多次运行值不同（抖动生效） |
| V6.4 | 停 Redis 后启动应用 | 重试后报 `redis not reachable` 并退出 |

```bash
git add internal/store internal/cache && git commit -m "feat(cache): 链接缓存与空值缓存"
```

---

## T7 仓储层与创建链路

**产出文件**
- Create: `internal/repo/link_repo.go`、`internal/service/link.go`、`internal/service/link_test.go`

**步骤**

1. 写失败测试，MySQL/Redis 均用测试替身（`fakeRepo` + `miniredis`）：

```go
package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"shortlink/internal/cache"
	"shortlink/internal/idgen"
)

type fakeRepo struct {
	rows map[string]string
	fail int // 前 N 次强制返回 ErrDuplicate
}

func (f *fakeRepo) Insert(ctx context.Context, seq uint64, code, url string) error {
	if f.fail > 0 {
		f.fail--
		return ErrDuplicate
	}
	if _, ok := f.rows[code]; ok {
		return ErrDuplicate
	}
	f.rows[code] = url
	return nil
}

func (f *fakeRepo) FindURLByCode(ctx context.Context, code string) (string, error) {
	if u, ok := f.rows[code]; ok {
		return u, nil
	}
	return "", ErrNotFound
}

func newSvc(t *testing.T) (*LinkService, *fakeRepo) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := idgen.New(context.Background(), rdb, "t:idgen", 10, 100000)
	if err != nil {
		t.Fatal(err)
	}
	repo := &fakeRepo{rows: map[string]string{}}
	return NewLinkService(repo, cache.NewLinkCache(rdb, time.Hour), g, "http://localhost:8080"), repo
}

func TestCreatePersistsAndCaches(t *testing.T) {
	svc, repo := newSvc(t)
	link, err := svc.Create(context.Background(), "https://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(link.Code) != 6 {
		t.Fatalf("code %q not 6 chars", link.Code)
	}
	if repo.rows[link.Code] != "https://example.com/x" {
		t.Fatal("not persisted")
	}
	if link.ShortURL != "http://localhost:8080/"+link.Code {
		t.Fatalf("short_url = %q", link.ShortURL)
	}
}

func TestCreateRetriesOnDuplicate(t *testing.T) {
	svc, repo := newSvc(t)
	repo.fail = 2
	if _, err := svc.Create(context.Background(), "https://example.com/y"); err != nil {
		t.Fatalf("should succeed after retries: %v", err)
	}
}

func TestCreateGivesUpAfterThreeDuplicates(t *testing.T) {
	svc, repo := newSvc(t)
	repo.fail = 3
	if _, err := svc.Create(context.Background(), "https://example.com/z"); !errors.Is(err, ErrTooManyRetries) {
		t.Fatalf("err = %v, want ErrTooManyRetries", err)
	}
}

func TestResolveUsesCacheThenRepo(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	link, _ := svc.Create(ctx, "https://example.com/a")

	got, err := svc.Resolve(ctx, link.Code)
	if err != nil || got != "https://example.com/a" {
		t.Fatalf("resolve cached: %q %v", got, err)
	}

	if _, err := svc.Resolve(ctx, "000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}
```

2. 运行确认失败。

3. `internal/repo/link_repo.go`：区分「唯一键冲突」与其它错误，用 MySQL 错误码 1062。

```go
package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
)

var ErrDuplicate = errors.New("repo: duplicate code")
var ErrNotFound = errors.New("repo: link not found")

type LinkRepo struct{ db *sql.DB }

func NewLinkRepo(db *sql.DB) *LinkRepo { return &LinkRepo{db: db} }

func (r *LinkRepo) Insert(ctx context.Context, seq uint64, code, url string) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO t_link (seq, code, url) VALUES (?, ?, ?)", seq, code, url)
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == 1062 {
		return ErrDuplicate
	}
	return fmt.Errorf("insert link: %w", err)
}

func (r *LinkRepo) FindURLByCode(ctx context.Context, code string) (string, error) {
	var url string
	err := r.db.QueryRowContext(ctx, "SELECT url FROM t_link WHERE code = ?", code).Scan(&url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query link: %w", err)
	}
	return url, nil
}
```

4. `internal/service/link.go`：编排取号、生成、落库、写缓存；缓存写失败不阻塞请求。

```go
package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"shortlink/internal/cache"
	"shortlink/internal/idgen"
	"shortlink/internal/repo"
	"shortlink/internal/shortcode"
)

var ErrNotFound = errors.New("service: link not found")
var ErrTooManyRetries = errors.New("service: too many duplicate retries")
var ErrIDGenUnavailable = errors.New("service: id generator unavailable")

const maxInsertRetry = 3

// LinkStore 是仓储的最小接口，便于测试替身。
type LinkStore interface {
	Insert(ctx context.Context, seq uint64, code, url string) error
	FindURLByCode(ctx context.Context, code string) (string, error)
}

type Link struct {
	Code     string `json:"code"`
	ShortURL string `json:"short_url"`
	URL      string `json:"url"`
}

type LinkService struct {
	repo    LinkStore
	cache   *cache.LinkCache
	idgen   *idgen.Generator
	baseURL string
}

func NewLinkService(repo LinkStore, c *cache.LinkCache, g *idgen.Generator, baseURL string) *LinkService {
	return &LinkService{repo: repo, cache: c, idgen: g, baseURL: baseURL}
}

func (s *LinkService) Create(ctx context.Context, rawURL string) (*Link, error) {
	for attempt := 0; attempt < maxInsertRetry; attempt++ {
		seq, err := s.idgen.Next(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIDGenUnavailable, err)
		}
		code, err := shortcode.Encode(seq)
		if err != nil {
			return nil, err
		}
		err = s.repo.Insert(ctx, seq, code, rawURL)
		if err == nil {
			if cerr := s.cache.Set(ctx, code, rawURL); cerr != nil {
				slog.Warn("cache set failed", "code", code, "err", cerr)
			}
			return &Link{Code: code, ShortURL: s.baseURL + "/" + code, URL: rawURL}, nil
		}
		if !errors.Is(err, repo.ErrDuplicate) {
			return nil, err
		}
		slog.Warn("duplicate code, retrying", "code", code, "attempt", attempt+1)
	}
	return nil, ErrTooManyRetries
}

func (s *LinkService) Resolve(ctx context.Context, code string) (string, error) {
	url, found, err := s.cache.Get(ctx, code)
	if err != nil {
		slog.Warn("cache get failed, fallback to db", "code", code, "err", err)
	} else if found {
		if url == "" { // 空值缓存
			return "", ErrNotFound
		}
		return url, nil
	}

	url, err = s.repo.FindURLByCode(ctx, code)
	if errors.Is(err, repo.ErrNotFound) {
		if nerr := s.cache.SetNegative(ctx, code); nerr != nil {
			slog.Warn("negative cache failed", "code", code, "err", nerr)
		}
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if serr := s.cache.Set(ctx, code, url); serr != nil {
		slog.Warn("cache backfill failed", "code", code, "err", serr)
	}
	return url, nil
}
```

5. 运行 `go test ./internal/service/ -race -v` → 4 条 PASS。

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V7.1 | `go test ./internal/service/ -race -v` | `TestCreatePersistsAndCaches`、`TestCreateRetriesOnDuplicate`、`TestCreateGivesUpAfterThreeDuplicates`、`TestResolveUsesCacheThenRepo` 全 PASS |
| V7.2 | `TestCreateRetriesOnDuplicate` | 前 2 次冲突后第 3 次成功（说明重试在循环内，不是直接抛错） |
| V7.3 | `TestCreateGivesUpAfterThreeDuplicates` | 3 次全冲突返回 `ErrTooManyRetries`，不无限重试 |
| V7.4 | 代码审查：`Resolve` 中 `cache.Get` 返回 err 时 | 落到 DB 分支，不向客户端报错（降级路径存在） |
| V7.5 | 代码审查：`Create` 中 `cache.Set` 失败 | 只 WARN 日志，仍返回成功（缓存非强依赖） |

```bash
git add internal/repo internal/service && git commit -m "feat(service): 创建与解析链路，含唯一键重试与缓存降级"
```

---

## T8 创建接口

**产出文件**
- Create: `internal/httpapi/errors.go`、`internal/httpapi/validate.go`、`internal/httpapi/link_handler.go`、`internal/httpapi/validate_test.go`、`internal/httpapi/router.go`

**步骤**

1. 先写 URL 校验的表驱动测试：

```go
package httpapi

import "testing"

func TestValidateURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"简单 https", "https://example.com", false},
		{"带路径查询", "https://example.com/a/b?c=1&d=2", false},
		{"http", "http://example.com", false},
		{"中文路径", "https://例子.测试/路径", false},
		{"空字符串", "", true},
		{"缺 scheme", "example.com", true},
		{"危险 scheme", "javascript:alert(1)", true},
		{"file scheme", "file:///etc/passwd", true},
		{"无 host", "https://", true},
		{"超长", "https://example.com/" + string(make([]byte, 2100)), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := validateURL(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateURL(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			}
		})
	}
}
```

2. 运行确认失败，然后实现 `validate.go`：

```go
package httpapi

import (
	"errors"
	"net/url"
)

var ErrInvalidURL = errors.New("url must be a valid http/https address within 2048 characters")

const maxURLLen = 2048

func validateURL(raw string) (string, error) {
	if raw == "" || len(raw) > maxURLLen {
		return "", ErrInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", ErrInvalidURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", ErrInvalidURL
	}
	if u.Host == "" {
		return "", ErrInvalidURL
	}
	return u.String(), nil
}
```

3. `internal/httpapi/errors.go`：统一错误体。

```go
package httpapi

import "github.com/gin-gonic/gin"

type errBody struct {
	Error errDetail `json:"error"`
}

type errDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func abortWith(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, errBody{Error: errDetail{Code: code, Message: msg}})
}
```

4. `link_handler.go` 的创建部分：

```go
const maxBodyBytes = 4 << 10

type createReq struct {
	URL string `json:"url"`
}

func (h *LinkHandler) Create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)

	var req createReq
	if err := c.ShouldBindJSON(&req); err != nil {
		abortWith(c, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body must be valid JSON within 4KB")
		return
	}
	u, err := validateURL(req.URL)
	if err != nil {
		abortWith(c, http.StatusBadRequest, "INVALID_URL", err.Error())
		return
	}
	link, err := h.svc.Create(c.Request.Context(), u)
	if errors.Is(err, service.ErrIDGenUnavailable) {
		abortWith(c, http.StatusServiceUnavailable, "IDGEN_UNAVAILABLE", "id generator unavailable, retry later")
		return
	}
	if err != nil {
		slog.Error("create link failed", "url", u, "err", err)
		abortWith(c, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	c.JSON(http.StatusCreated, link)
}
```

5. `router.go` 注册（此时只挂创建与首页，跳转留到 T9）：

```go
func NewRouter(h *LinkHandler, webFS fs.FS) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), requestLogger())
	r.POST("/api/v1/shorten", h.Create)
	r.GET("/healthz", h.Healthz)
	r.GET("/", serveIndex(webFS))
	return r
}
```

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V8.1 | `go test ./internal/httpapi/ -run TestValidateURL -v` | 10 个子测试全 PASS（含 `javascript:` 与 `file://` 被拒） |
| V8.2 | `DB_DSN=... go run ./cmd/server` 后发 `curl -sS -o /dev/null -w '%{http_code}' -X POST localhost:8080/api/v1/shorten -H 'Content-Type: application/json' -d '{"url":"https://example.com/a"}'` | `201` |
| V8.3 | 同上，`-d '{"url":"ftp://x"}'` | `400`，body 含 `"code":"INVALID_URL"` |
| V8.4 | 同上，`-d '{"url":123}'` | `400`，`INVALID_URL`（类型不匹配不 panic） |
| V8.5 | 发 5KB body | `413`，`PAYLOAD_TOO_LARGE` |
| V8.6 | 响应体字段 | 恰为 `code`（6 位）、`short_url`、`url`，且 `short_url = BASE_URL + "/" + code` |
| V8.7 | MySQL 中 `SELECT COUNT(*) FROM t_link` | 每次成功请求 +1 |

```bash
git add internal/httpapi && git commit -m "feat(api): 创建短链接口与统一错误响应"
```

---

## T9 跳转接口（短码 `GET /:code`）

> 实施时由「`NoRoute` 兜底」改为「直接注册 `GET /:code` + `HEAD /:code`」，见偏差记录第 20 条。

**产出文件**
- Modify: `internal/httpapi/link_handler.go`、`internal/httpapi/router.go`
- Create: `internal/httpapi/redirect_test.go`

**步骤**

1. 写失败测试，用 `httptest` 打真实路由（含冲突回归）：

```go
package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

func TestRedirectAndRouteConflicts(t *testing.T) {
	h, _ := newTestHandler(t)
	r := NewRouter(h, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>ok</html>")}})

	// 先创建一条
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/shorten", bodyOf(`{"url":"https://example.com/target"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status %d", w.Code)
	}
	code := extractCode(t, w.Body.Bytes())

	tests := []struct {
		name     string
		path     string
		wantCode int
		wantLoc  string
	}{
		{"跳转命中", "/" + code, http.StatusFound, "https://example.com/target"},
		{"非法短码", "/abc", http.StatusNotFound, ""},
		{"不存在的短码", "/000000", http.StatusNotFound, ""},
		{"API 优先于短码", "/api/v1/shorten", http.StatusMethodNotAllowed, ""},
		{"健康检查不被短码吞掉", "/healthz", http.StatusOK, ""},
		{"首页", "/", http.StatusOK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if w.Code != tc.wantCode {
				t.Fatalf("%s status %d, want %d", tc.path, w.Code, tc.wantCode)
			}
			if tc.wantLoc != "" && w.Header().Get("Location") != tc.wantLoc {
				t.Fatalf("Location %q", w.Header().Get("Location"))
			}
		})
	}
}
```

2. 运行：**不会 panic**。实测 gin v1.10.0，`GET /:code` 与 `POST /api/v1/shorten` 可以共存，静态路径优先匹配。

> 订正：本计划初稿断言此处会因 wildcard 冲突 panic，该断言在 gin v1.10 上**不成立**（gin 的 patricia 树允许同层「静态兄弟 + 通配」，只有旧的 httprouter 才会 panic）。方案仍选 `NoRoute`，但理由改为可维护性而非避坑，详见设计方案 §3.3 的实测对照表。

3. 在 `router.go` 用 `NoRoute` 兜底，并**不注册** `/:code`：

```go
r.NoRoute(shortcodeFallback(h))
```

4. `link_handler.go` 增加跳转处理：

```go
func (h *LinkHandler) Redirect(c *gin.Context) {
	code := strings.TrimPrefix(c.Request.URL.Path, "/")
	if !shortcode.IsValidCode(code) {
		c.String(http.StatusNotFound, "404 page not found")
		return
	}
	url, err := h.svc.Resolve(c.Request.Context(), code)
	if errors.Is(err, service.ErrNotFound) {
		c.String(http.StatusNotFound, "404 page not found")
		return
	}
	if err != nil {
		slog.Error("resolve failed", "code", code, "err", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, url)
}

// shortcodeFallback 只被未匹配任何路由的请求触发，
// 因此 /api/v1/shorten、/healthz、/ 天然优先，不存在路由冲突。
func shortcodeFallback(h *LinkHandler) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.String(http.StatusNotFound, "404 page not found")
			return
		}
		h.Redirect(c)
	}
}
```

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V9.1 | 单独用 `r.GET("/:code")` 注册一次 | 注册成功、不 panic；`GET /healthz` 命中静态、`GET /aB3c9F` 命中通配——二者可共存（初稿的"会 panic"假设已被实测推翻） |
| V9.2 | `go test ./internal/httpapi/ -run TestRedirect -v` | 全部子测试 PASS（含单段 `/api`、多段 `/a/b` 均为 404） |
| V9.3 | `curl -sSI localhost:8080/<code>` | `HTTP/1.1 302 Found`，`Location:` 为原 URL，含 `Cache-Control: no-store` |
| V9.4 | `curl -sS -o /dev/null -w '%{http_code}' localhost:8080/api/v1/shorten` | `405`（GET 打 POST 路由），**不是 404**，证明 API 未被短码吞掉 |
| V9.5 | `curl -sS -o /dev/null -w '%{http_code}' localhost:8080/healthz` | `200` |
| V9.6 | `curl -sS -o /dev/null -w '%{http_code}' localhost:8080/abc` | `404`（长度不足 6） |
| V9.7 | 请求 `/000000` 后再查 Redis | 存在 `link:000000` 且 TTL ≤ 60s（空值缓存生效） |
| V9.8 | 手工 `DEL link:<code>` 后再次访问 | Redis 回填为原 URL，TTL 约 24h，请求仍 302 |

```bash
git add internal/httpapi && git commit -m "feat(api): 短码跳转走 NoRoute 兜底，避免与 API 路由冲突"
```

---

## T10 内嵌前端页面

**产出文件**
- Create: `web/index.html`、`internal/httpapi/static.go`

**步骤**

1. `web/index.html`：单页、无依赖、`fetch` 调 API，含复制按钮与错误提示。

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>短链接</title>
<style>
  :root { color-scheme: light dark; }
  * { box-sizing: border-box; }
  body { margin: 0; min-height: 100vh; display: grid; place-items: center;
         font: 16px/1.6 system-ui, -apple-system, "Segoe UI", "Microsoft YaHei", sans-serif;
         background: #0f1115; color: #e8eaed; padding: 24px; }
  main { width: 100%; max-width: 560px; }
  h1 { font-size: 28px; margin: 0 0 8px; }
  p.sub { margin: 0 0 28px; color: #8b93a1; font-size: 14px; }
  form { display: flex; gap: 8px; }
  input[type=url] { flex: 1; min-width: 0; padding: 12px 14px; border-radius: 10px;
         border: 1px solid #2b313c; background: #171a21; color: inherit; font-size: 15px; }
  input[type=url]:focus { outline: 2px solid #4c8dff; border-color: transparent; }
  button { padding: 12px 20px; border: 0; border-radius: 10px; background: #4c8dff;
         color: #fff; font-size: 15px; font-weight: 600; cursor: pointer; white-space: nowrap; }
  button:disabled { opacity: .55; cursor: not-allowed; }
  #result { margin-top: 24px; padding: 16px; border-radius: 12px; background: #171a21;
         border: 1px solid #2b313c; display: none; }
  #result.show { display: block; }
  #short { font-size: 20px; font-weight: 600; color: #6ea8ff; word-break: break-all;
         text-decoration: none; display: inline-block; margin-bottom: 8px; }
  #orig { font-size: 13px; color: #8b93a1; word-break: break-all; }
  .row { display: flex; gap: 8px; align-items: center; justify-content: space-between; }
  .copy { padding: 6px 12px; font-size: 13px; }
  #error { margin-top: 16px; color: #ff7a7a; font-size: 14px; min-height: 20px; }
</style>
</head>
<body>
<main>
  <h1>短链接</h1>
  <p class="sub">粘贴长链接，生成 6 位短码。</p>
  <form id="f">
    <input id="url" type="url" placeholder="https://example.com/very/long/url" required autocomplete="off">
    <button id="submit" type="submit">生成</button>
  </form>
  <div id="error"></div>
  <div id="result">
    <div class="row">
      <a id="short" href="#" target="_blank" rel="noopener"></a>
      <button class="copy" id="copy" type="button">复制</button>
    </div>
    <div id="orig"></div>
  </div>
</main>
<script>
const f = document.getElementById('f');
const urlInput = document.getElementById('url');
const submit = document.getElementById('submit');
const result = document.getElementById('result');
const shortA = document.getElementById('short');
const orig = document.getElementById('orig');
const errBox = document.getElementById('error');
const copyBtn = document.getElementById('copy');

f.addEventListener('submit', async (e) => {
  e.preventDefault();
  errBox.textContent = '';
  result.classList.remove('show');
  const url = urlInput.value.trim();
  if (!url) return;

  submit.disabled = true;
  submit.textContent = '生成中…';
  try {
    const res = await fetch('/api/v1/shorten', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ url })
    });
    const data = await res.json();
    if (!res.ok) {
      errBox.textContent = (data.error && data.error.message) || '生成失败';
      return;
    }
    shortA.textContent = data.short_url;
    shortA.href = data.short_url;
    orig.textContent = data.url;
    result.classList.add('show');
  } catch (err) {
    errBox.textContent = '网络错误，请重试';
  } finally {
    submit.disabled = false;
    submit.textContent = '生成';
  }
});

copyBtn.addEventListener('click', async () => {
  try {
    await navigator.clipboard.writeText(shortA.textContent);
    copyBtn.textContent = '已复制';
    setTimeout(() => { copyBtn.textContent = '复制'; }, 1500);
  } catch {
    errBox.textContent = '复制失败，请手动选择';
  }
});
</script>
</body>
</html>
```

2. `internal/httpapi/static.go`：把 `web/` 嵌进二进制（`embed` 只能嵌本包目录，故把文件放到 `internal/httpapi/web/index.html`，或在该包建立指向 `web/` 的副本目录——**采用前者，`internal/httpapi/web/index.html` 为唯一来源**）。

```go
package httpapi

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed all:web
var webFS embed.FS

func WebFS() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	return sub
}

func serveIndex(fsys fs.FS) gin.HandlerFunc {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		panic("index.html missing from embedded FS: " + err.Error())
	}
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	}
}
```

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V10.1 | `go build ./...` | 0 退出码（embed 路径正确） |
| V10.2 | `curl -sS localhost:8080/ \| head -5` | 返回 HTML，含 `<title>短链接</title>` |
| V10.3 | 浏览器打开 `http://localhost:8080/`，输入 `https://example.com/a/b?c=1` 点「生成」 | 显示 6 位短链接与原链接，无控制台报错 |
| V10.4 | 点「复制」 | 按钮变「已复制」，剪贴板为该短链 |
| V10.5 | 输入 `ftp://x` 提交 | 页面显示后端返回的 `url must be a valid http/https...`，不弹 alert |
| V10.6 | 点结果中的短链接 | 新标签页 302 到原地址 |
| V10.7 | 断网/停服后点「生成」 | 显示「网络错误，请重试」，按钮恢复可点 |

```bash
git add internal/httpapi/web internal/httpapi/static.go && git commit -m "feat(web): 内嵌单页前端"
```

---

## T11 健康检查与中间件

**产出文件**
- Create: `internal/httpapi/middleware.go`、`internal/httpapi/health.go`
- Modify: `cmd/server/main.go`（完整装配）

**步骤**

1. `middleware.go`：请求日志（带 `request_id`）+ recover 已由 `gin.Recovery` 承担。

```go
package httpapi

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		rid := c.GetHeader("X-Request-Id")
		if rid == "" {
			rid = time.Now().Format("20060102150405.000000")
		}
		c.Set("request_id", rid)
		c.Writer.Header().Set("X-Request-Id", rid)

		c.Next()

		slog.Info("http",
			"rid", rid,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"ms", time.Since(start).Milliseconds(),
		)
	}
}
```

2. `health.go`：并行探测 MySQL/Redis，任一失败返回 503。

```go
package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

func (h *LinkHandler) Healthz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()

	mysqlOK, redisOK := "ok", "ok"
	status := http.StatusOK

	if err := h.db.PingContext(ctx); err != nil {
		mysqlOK, status = "down", http.StatusServiceUnavailable
	}
	if err := h.rdb.Ping(ctx).Err(); err != nil {
		redisOK, status = "down", http.StatusServiceUnavailable
	}
	c.JSON(status, gin.H{"status": map[bool]string{true: "ok", false: "degraded"}[status == http.StatusOK],
		"mysql": mysqlOK, "redis": redisOK})
}
```

3. `cmd/server/main.go` 完整装配：config → 日志 → MySQL(+迁移) → Redis → 号段（用 `MaxSeq` 兜底种子）→ router → 优雅关闭。

```go
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"shortlink/internal/cache"
	"shortlink/internal/config"
	"shortlink/internal/httpapi"
	"shortlink/internal/idgen"
	"shortlink/internal/repo"
	"shortlink/internal/service"
	"shortlink/internal/store"
)

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bootCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	db, err := store.OpenMySQL(bootCtx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(bootCtx, db); err != nil {
		return err
	}

	rdb, err := store.OpenRedis(bootCtx, cfg.RedisAddr)
	if err != nil {
		return err
	}
	defer rdb.Close()

	// 用库中 MAX(seq) 兜底：Redis 数据丢失时不会从 0 重发导致撞号
	maxSeq, err := store.MaxSeq(bootCtx, db)
	if err != nil {
		return err
	}
	start := cfg.SeqStart
	if maxSeq >= start {
		start = maxSeq + 1
	}
	gen, err := idgen.New(bootCtx, rdb, "shortlink:idgen", cfg.SegmentSize, start)
	if err != nil {
		return err
	}

	svc := service.NewLinkService(repo.NewLinkRepo(db), cache.NewLinkCache(rdb, cfg.CacheTTL), gen, cfg.BaseURL)
	handler := httpapi.NewLinkHandler(svc, db, rdb)
	router := httpapi.NewRouter(handler, httpapi.WebFS())

	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server started", "addr", cfg.HTTPAddr, "base_url", cfg.BaseURL, "idgen_start", start)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	slog.Info("shutting down")
	return srv.Shutdown(shutdownCtx)
}
```

> `flag` 在此文件未使用则删掉该 import，避免编译失败。

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V11.1 | `go vet ./...` + `go build ./...` | 0 退出码，无未使用 import |
| V11.2 | `curl -sS localhost:8080/healthz` | `200`，body `{"mysql":"ok","redis":"ok","status":"ok"}` |
| V11.3 | `docker stop` Redis 容器后 `curl -sS -o /dev/null -w '%{http_code}' localhost:8080/healthz` | `503`，`redis":"down"` |
| V11.4 | Redis 停掉时访问已缓存过的短码 | 仍 `302`（降级直读 MySQL，可用性优先） |
| V11.5 | 任意请求的响应头 | 含 `X-Request-Id`；服务端日志含同 `rid` 与 `ms` |
| V11.6 | 发 `Ctrl+C`（或 `docker stop`） | 日志 `shutting down`，进程 10s 内正常退出，无 goroutine panic |
| V11.7 | 空库首次启动日志 | `idgen_start=100000` |
| V11.8 | 已有数据时重启 | `idgen_start = MAX(seq)+1`，接着创建不撞码 |

```bash
git add cmd internal && git commit -m "feat: 健康检查、请求日志与优雅关闭"
```

---

## T12 容器化与编排

**产出文件**
- Create: `Dockerfile`、`docker-compose.yml`、`nginx/nginx.conf`、`.dockerignore`

**步骤**

1. `Dockerfile`（多阶段、静态二进制、非 root）：

```dockerfile
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /out/server /usr/local/bin/server
USER app
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["server"]
```

2. `.dockerignore`：`docs/`、`.git/`、`*_test.go` 之外的全保留；排除 `mysql-data`、`redis-data`。

3. `docker-compose.yml`：

```yaml
services:
  mysql:
    image: mysql:8.0
    environment:
      MYSQL_ROOT_PASSWORD: root
      MYSQL_DATABASE: shortlink
      MYSQL_USER: shortlink
      MYSQL_PASSWORD: shortlink
    volumes:
      - mysql-data:/var/lib/mysql
    healthcheck:
      test: ["CMD", "mysqladmin", "ping", "-h", "127.0.0.1", "-uroot", "-proot"]
      interval: 5s
      timeout: 3s
      retries: 20

  redis:
    image: redis:7-alpine
    command: ["redis-server", "--appendonly", "yes"]
    volumes:
      - redis-data:/data
    healthcheck:
      test: ["CMD", "redis-cli", "ping"]
      interval: 5s
      timeout: 3s
      retries: 20

  app:
    build: .
    environment:
      HTTP_ADDR: ":8080"
      BASE_URL: "http://localhost:8080"
      DB_DSN: "shortlink:shortlink@tcp(mysql:3306)/shortlink?parseTime=true&charset=utf8mb4&loc=Local"
      REDIS_ADDR: "redis:6379"
      CACHE_TTL_SECONDS: "86400"
      SEGMENT_SIZE: "1000"
      SEQ_START: "100000"
    ports:
      - "8080:8080"
    depends_on:
      mysql:
        condition: service_healthy
      redis:
        condition: service_healthy
    restart: unless-stopped

  nginx:
    image: nginx:1.27-alpine
    profiles: ["proxy"]
    ports:
      - "80:80"
    volumes:
      - ./nginx/nginx.conf:/etc/nginx/conf.d/default.conf:ro
    depends_on:
      - app
    restart: unless-stopped

volumes:
  mysql-data:
  redis-data:
```

4. `nginx/nginx.conf`：

```nginx
server {
    listen 80;
    server_name _;
    client_max_body_size 16k;

    location / {
        proxy_pass http://app:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_http_version 1.1;
    }
}
```

**验证点**

| 编号 | 操作 | 预期 |
|---|---|---|
| V12.1 | `docker compose config` | 解析成功，无告警 |
| V12.2 | `docker compose build` | 成功，最终镜像 < 30MB（`docker images` 查看） |
| V12.3 | `docker compose up -d` 后 `docker compose ps` | 3 个服务 `running`/`healthy` |
| V12.4 | 启动顺序 | 日志中 app 启动时间晚于 mysql/redis 变为 healthy |
| V12.5 | `docker exec -it <app> id` | uid=10001(app)，非 root |
| V12.6 | `docker compose down && docker compose up -d` | 数据保留（短码仍可跳转，说明卷持久化生效） |
| V12.7 | `docker compose --profile proxy up -d` 后 `curl -sSI localhost/<code>` | `302`，经 nginx 转发，`X-Request-Id` 存在 |
| V12.8 | `docker compose stop mysql` 后 `curl localhost:8080/healthz` | `503` |

```bash
git add Dockerfile docker-compose.yml nginx .dockerignore
git commit -m "chore: 容器化与 docker compose 编排"
```

---

## T13 端到端验收

**产出文件**
- Create: `scripts/verify.sh`、`README.md`

**步骤**

1. `scripts/verify.sh`（在容器/CI 的 bash 下运行）：

```bash
#!/usr/bin/env bash
set -euo pipefail
BASE="${BASE:-http://localhost:8080}"

echo "== 1. 健康检查"
curl -fsS "$BASE/healthz" | grep -q '"status":"ok"'

echo "== 2. 创建短链"
RESP=$(curl -fsS -X POST "$BASE/api/v1/shorten" \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/a/very/long/path?x=1&y=2"}')
CODE=$(printf '%s' "$RESP" | sed -n 's/.*"code":"\([^"]*\)".*/\1/p')
[ "${#CODE}" -eq 6 ] || { echo "bad code: $CODE"; exit 1; }
echo "   code=$CODE"

echo "== 3. 跳转"
LOC=$(curl -sS -o /dev/null -w '%{redirect_url}' "$BASE/$CODE")
[ "$LOC" = "https://example.com/a/very/long/path?x=1&y=2" ] || { echo "bad location: $LOC"; exit 1; }

echo "== 4. 非法 URL"
STATUS=$(curl -sS -o /dev/null -w '%{http_code}' -X POST "$BASE/api/v1/shorten" \
  -H 'Content-Type: application/json' -d '{"url":"javascript:alert(1)"}')
[ "$STATUS" = "400" ] || { echo "want 400 got $STATUS"; exit 1; }

echo "== 5. 不存在短码"
STATUS=$(curl -sS -o /dev/null -w '%{http_code}' "$BASE/000000")
[ "$STATUS" = "404" ] || { echo "want 404 got $STATUS"; exit 1; }

echo "== 6. 并发 1000 条无重复"
tmp=$(mktemp)
seq 1 1000 | xargs -P 32 -I{} curl -sS -X POST "$BASE/api/v1/shorten" \
  -H 'Content-Type: application/json' -d '{"url":"https://example.com/c/{}"}' \
  | sed -n 's/.*"code":"\([^"]*\)".*/\1/p' > "$tmp"
total=$(wc -l < "$tmp" | tr -d ' ')
uniq=$(sort -u "$tmp" | wc -l | tr -d ' ')
echo "   total=$total unique=$uniq"
[ "$total" = "1000" ] && [ "$uniq" = "1000" ] || { echo "duplicate codes!"; exit 1; }
rm -f "$tmp"

echo "== ALL PASSED"
```

2. `README.md`：一句话说明、`docker compose up -d`、两个 curl 示例、配置表、测试命令。

**验证点（全部通过即验收）**

| 编号 | 操作 | 预期 |
|---|---|---|
| V13.1 | `bash scripts/verify.sh` | 输出 6 段 `==` 与 `ALL PASSED`，退出码 0 |
| V13.2 | 步骤 6 并发创建 | `total=1000 unique=1000`（号段 + 双射 + 唯一键三重保障有效） |
| V13.3 | `go test ./... -race -count=1` | 全部 PASS |
| V13.4 | `go vet ./...` | 无告警 |
| V13.5 | 新机器克隆后仅执行 `docker compose up -d` | 3 分钟内可用，无需手工建表/装依赖 |
| V13.6 | `docker compose down -v && up -d` 后重跑 verify | 通过（空库冷启动无残留依赖） |

```bash
git add scripts README.md && git commit -m "test: 端到端验收脚本与使用说明"
```

---

## 验收清单（交付前逐条勾）

- [ ] 短码恒为 6 位、字符集 `0-9a-zA-Z`、20 万样本零重复
- [ ] 相邻 `seq` 的短码无可观察规律（打散有效）
- [ ] `POST /api/v1/shorten` 与 `GET /{code}` 均符合第 5 节契约
- [ ] `/api/v1/shorten`、`/healthz`、`/` 不被短码路由吞掉
- [ ] 缓存命中路径不查 MySQL（可临时关库验证 302 仍返回）
- [ ] Redis 停机时跳转可用、创建返回 503（明确降级边界）
- [ ] `docker compose up -d` 一条命令冷启动成功
- [ ] 重启后发号不回退、不撞码
- [ ] 无 `slog` 之外的日志库、无 ORM、无前端构建链（YAGNI 守住了）

## 风险与回滚

| 风险 | 影响 | 处理 |
|---|---|---|
| `embed` 路径写错 | 编译失败 | T5/T10 的 V 项已用 `go build` 卡住 |
| `seq` 超 2^32 | 无法发号 | `ErrSeqOverflow` 明确报错；42 亿条余量，本期不触达 |
| Redis 全量丢失且 DB 为空 | 无影响 | `SEQ_START` 保证起点；有数据时用 `MAX(seq)` 兜底 |
| 迁移脚本被改坏 | 启动失败 | `CREATE TABLE IF NOT EXISTS` 幂等，改表必须新增迁移文件，不复用 0001 |
| 回滚 | — | 镜像按 tag 保留上一版；数据表只增不改，回滚旧镜像无需回滚 schema |

---

## 实施偏差记录

执行本计划时与原稿的差异，以及每处的实测依据。

| # | 计划原文 | 实际实现 | 原因 / 依据 |
|---|---|---|---|
| 1 | Go 1.24 + `golang:1.24-alpine` | go 1.27.3 + `golang:1.27-alpine` | 与本机 toolchain 对齐，避免工具链降级 |
| 2 | `mysql:8.0` | `mysql:8.4` | 8.4 为 8.x LTS，`utf8mb4_0900_ai_ci` 支持不变 |
| 3 | `migrations/`、`web/` 放仓库根 | `internal/store/migrations/`、`internal/httpapi/web/` | `go:embed` 只能嵌入当前包目录内文件，放根目录无法编译 |
| 4 | `GET /api/v1/shorten` → 405 | 需显式 `r.HandleMethodNotAllowed = true` | 默认该请求会落到 `NoRoute` 变成 404；开启后与 `/{code}` 区分清晰 |
| 5 | 请求体类型错误 → `INVALID_URL` | `INVALID_REQUEST` | 与「URL 内容非法」区分，前端提示更准确 |
| 6 | 号段初值 `start` | `start-1` | 计数器语义是「已分配到的最大号」，否则首个短码会跳过 `SEQ_START` 浪费 1 个号 |
| 7 | 仅靠客户端超时配置 | 客户端收紧 + `LinkCache`/`idgen` 各加 500ms 硬上限 | 实测 Redis 停机时一次跳转被拖到 **19.6s** 并被 `WriteTimeout` 掐断（`curl 52 Empty reply`）。修复后：跳转 `302 / 1.0s`、创建 `0.5s` |
| 8 | 「Redis 停机 → 创建必 503」 | 号段缓冲内仍可创建（实测 `201 / 0.5s`），号段耗尽才 `503`（实测 `0.5s`） | 号段预分配的固有特性，属预期行为；用 `SEGMENT_SIZE=1` 的独立实例验证了 503 分支 |
| 9 | 每任务单独提交 | T5/T6 合并、T7 单独、API 与前端合并 | `go:embed` 与路由互相依赖，强行拆分会产生不可编译的中间提交 |
| 10 | 全部测试带 `-race` | 本机无 gcc，改在 `golang:1.27-alpine` 容器内 `apk add gcc musl-dev` 后执行 | 容器内 `-race` 全绿；顺带发现并修正 `TestTTLJitterApplied` 断言（抖动上限是 1.1×TTL，原断言 `≤1h` 过严） |
| 11 | 号段申请在锁内完成 | 双检查 + 独立 `fetchMu` + 1s 失败冷却 | **代码审查 Blocking**：原实现持锁做网络 I/O，Redis 故障且号段耗尽时 32 并发 ≈ 32×500ms=16s，超过 `WriteTimeout` 被掐断（`curl 52`）。修复后实测 32 并发全 503，总耗时 **511ms** |
| 12 | `SETNX(key, start-1)` 初始化计数器 | Lua `EVAL`：`if cur < floor then SET` | Redis 从**陈旧 AOF** 恢复时计数器可能落后于库里 `MAX(seq)`，`SETNX` 不会修正 → 重发旧号 → 撞 `uk_seq`。Lua 保证只能抬升、绝不回退 |
| 13 | 迁移固定执行 `0001_init.sql` | 遍历全部 `migrations/*.sql` 并按名排序执行 | 原来只跑第一个文件，后续新增迁移会被静默忽略（无 `schema_migrations` 表），与「变更必须新增迁移文件」的注释自相矛盾 |
| 14 | 单一 `/healthz` | 拆 `/livez`（不碰依赖）与 `/healthz`（就绪，含依赖） | 原设计下 Redis 抖动会让容器 `unhealthy`，任何用 HTTP 探针做 liveness 的编排都会重启一个仍在正常跳转的进程 |
| 15 | `testcontainers-go` 集成测试 | `//go:build integration` + `TEST_MYSQL_DSN`，在 compose 的 MySQL 上跑 | 已实测 6 条全通过（含 1062→`ErrDuplicate`、空表 `MaxSeq`、长 URL 往返）；不额外引入 testcontainers 依赖 |
| 16 | 日志含完整 URL | 只记 `url_len` | URL 的 query 常带 token / 一次性凭证，属日志侧敏感信息泄漏 |
| 17 | 设计 §7 提到 CORS 中间件 | 未实现 | 同源单页部署，YAGNI |
| 18 | — | 全仓库行尾统一 LF + `.gitattributes` | Windows 检出为 CRLF 时 `gofmt -l` 会把 24 个文件全部标红，掩盖真实格式问题；`scripts/verify.sh` 也会因 CRLF 在 Linux 下报错 |
| 19 | 文档断言 `/:code` 与 `/api/v1/shorten` **冲突会 panic** | 文档改为「两者**不冲突**」，实测表见设计方案 §3.3 | **原假设错误**。实测 gin v1.10.0：`GET /:code` + `POST /api/v1/shorten`、`GET /:code` + `GET /healthz`、`GET /:code` + `GET /api/v1/shorten`、`GET /api/v1/shorten` + `GET /api/:id` 四种组合均注册成功。gin 的 patricia 树允许同层「静态兄弟 + 通配」，只有旧版 httprouter 才 panic |
| 20 | 短码走 `NoRoute` 兜底 | 直接注册 `GET /:code` + `HEAD /:code`，`Redirect` 用 `c.Param("code")` 取参 | 既然不冲突，直连注册更直白：`POST /aB3c9F` 由框架给 405（而不是 404），支持 HEAD，且不必手工从 `URL.Path` 裁剪。`TestParamRouteCoexistsWithStaticRoutes` 改为在真实路由上断言「`/healthz` 未被困配遮蔽 + 合法短码走到跳转」，作为升级哨兵 |

### 实测结论（`docker compose` + MySQL 8.4 + Redis 7）

```
== 1. 健康检查      {"mysql":"ok","redis":"ok","status":"ok"}
== 2. 创建短链      code=673Fs2
== 3. 跳转          location=https://example.com/a/very/long/path?x=1&y=2
== 4. 非法 URL 被拒 400
== 5. 不存在的短码  404
== 6. API 路径不被吞 405
== 7. 并发 1000 条  total=1000 created=1000 unique=1000
== ALL PASSED
```

补充验证项：

| 场景 | 结果 |
|---|---|
| 空值缓存 | `link:000000` TTL=60s，值空串 |
| 缓存被删后回源 | `DEL` 后请求 → 302，TTL 回填约 24h |
| 运行用户 | `uid=10001(app)`，非 root |
| 镜像体积 | 28.3 MB |
| `compose down` 后再 `up` | 数据保留（4218 行），旧短码仍 302 |
| nginx profile | 经 `:80` 反代 302，`/healthz` 正常 |
| 重启后发号 | 日志 `idgen ready start=103208 db_max_seq=103207`，不回退不撞码 |
| Redis 停机 | `/healthz` 503（2s）、跳转仍 302（1s，降级直读 MySQL）、创建在号段缓冲内 201 |
| 号段耗尽 + Redis 停机 | 单请求 503（0.5s）；**32 并发全 503，总耗时 511ms**（修复前 ≈16s） |
| repo 集成测试 | 6 条全通过（唯一键 1062 归一、空表 `MaxSeq`、长中文 URL 往返） |
| `go test -race ./internal/...`（容器内） | 全绿 |


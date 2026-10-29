# 短链接系统

Go(Gin) + MySQL + Redis 的最小闭环短链服务：创建短链、访问跳转。短码由「Redis 号段自增 + Feistel 双射打散」生成，定长 6 位，全局唯一且不可枚举。

前端是一个内嵌在二进制里的单页 HTML，无需 Node 构建。

## 快速开始

```bash
docker compose up -d --build
```

服务默认监听 `http://localhost:8080`：

| 方法 | 路径 | 说明 |
|---|---|---|
| `POST` | `/api/v1/shorten` | 创建短链 |
| `GET` | `/{code}` | 302 跳转到原始 URL |
| `GET` | `/` | 内嵌首页 |
| `GET` | `/healthz` | 就绪探针（探测 MySQL + Redis） |
| `GET` | `/livez` | 存活探针（不碰依赖，永远 200） |

```bash
# 创建
curl -X POST localhost:8080/api/v1/shorten \
  -H 'Content-Type: application/json' \
  -d '{"url":"https://example.com/a/very/long/path"}'
# {"code":"8vKuoi","short_url":"http://localhost:8080/8vKuoi","url":"https://example.com/a/very/long/path"}

# 跳转
curl -sSI localhost:8080/8vKuoi
# HTTP/1.1 302 Found
# Location: https://example.com/a/very/long/path
```

浏览器直接打开 `http://localhost:8080/` 即可用页面创建。

可选的反向代理（80 端口）：

```bash
docker compose --profile proxy up -d
```

## 配置

全部通过环境变量注入，`docker-compose.yml` 已给出默认值。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `BASE_URL` | `http://localhost:8080` | 拼装 `short_url` 用 |
| `DB_DSN` | — | 必填，MySQL DSN |
| `REDIS_ADDR` | `localhost:6379` | Redis 地址 |
| `CACHE_TTL_SECONDS` | `86400` | 链接缓存 TTL（带 ±10% 抖动） |
| `SEGMENT_SIZE` | `1000` | 号段大小 |
| `SEQ_START` | `100000` | 空库时的起始序列 |

## 本地开发

```bash
go test ./...              # 单元测试（miniredis 代替真实 Redis，无需外部依赖）
go build ./cmd/server
DB_DSN='user:pass@tcp(127.0.0.1:3306)/shortlink?parseTime=true&charset=utf8mb4' \
  REDIS_ADDR=127.0.0.1:6379 go run ./cmd/server

# 竞态检测（需要 C 编译器；Windows 无 gcc 时可在容器内跑）
go test -race ./internal/...

# 仓储层集成测试（需要真实 MySQL，会清空 t_link，务必指向一次性库）
docker compose up -d mysql
TEST_MYSQL_DSN='shortlink:shortlink@tcp(mysql:3306)/shortlink?parseTime=true&charset=utf8mb4' \
  go test -tags integration ./internal/repo/ -v
```

端到端验收（服务已启动）：

```bash
BASE=http://localhost:8080 bash scripts/verify.sh
```

脚本会校验健康检查、创建、跳转、非法 URL、404、路由不被短码吞掉，以及并发创建 1000 条短码无重复。

## 设计要点

- **发号**：Redis `INCRBY` 号段预分配（默认 1000），进程内游标消费完才访问 Redis。重启丢弃剩余号段，只浪费号、不重号；启动时用库中 `MAX(seq)` 兜底，Redis 数据清空也不会从 0 重发。
- **打散**：`seq → 4 轮 16 位 Feistel → 加偏移取模 → base62`。Feistel 是双射，因此不同 `seq` 必得不同短码，**创建时不需要查重**；偏移量 `2^32` 把输出推到 `62^5` 之上，短码恒为 6 位。相邻号段生成的短码形如 `8vKuoi` / `5WqvM7` / `7shSqW`，无法顺推枚举。
- **路由**：短码走 Gin 的 `NoRoute` 兜底，短链保持 `/{code}` 最短形态；`/api/v1/shorten` 等真实路由天然优先，避免 `/:code` 通配导致的 panic。
- **降级边界**：Redis 挂了，跳转降级直读 MySQL（仍 302），`/healthz` 变 503，`/livez` 仍 200（避免编排重启一个还在正常服务的进程）；创建在号段缓冲内仍可用，号段耗尽则返回 503（唯一性不能被降级牺牲）。
- **快速失败**：缓存与取号操作都有 500ms 硬上限；取号另有 1s 失败冷却 + 串行化，Redis 故障时 32 个并发请求总耗时约 0.5s，而不是 `32 × 超时`。
- **一致性**：链接创建后不可变，没有更新路径，因此不存在缓存失效问题。

详见 [设计方案](docs/plans/2026-10-29-shortlink-design.md) 与 [实施计划](docs/plans/2026-10-29-shortlink-impl.md)。

# 短链接系统 设计方案

- 日期：2026-10-29
- 范围：最小闭环（创建短链 + 访问跳转）
- 技术栈：Go 1.24（Gin）+ MySQL 8 + Redis 7 + 原生 HTML/JS（go:embed）+ Docker Compose

---

## 1. 目标与非目标

### 目标

1. `POST /api/v1/shorten` 接收长 URL，返回短码与短链接。
2. `GET /{code}` 302 跳转到原始 URL。
3. 短码由「自增序列 + 可逆打散」生成：全局唯一、无碰撞检查、不可枚举、定长 6 位。
4. 读路径由 Redis 承担，命中时 p99 < 5ms。
5. `docker compose up` 一条命令拉起全部依赖。

### 非目标（本期明确不做）

- 访问量统计 / 报表
- 自定义短码、过期时间、删除、编辑
- 用户体系、鉴权、限流配额
- 同一 URL 复用（幂等去重）
- 布隆过滤器、多级缓存、分库分表

> 上述能力在数据模型和接口版本号（`/api/v1`）上留了扩展位，但不写实现。

---

## 2. 总体架构

```
                ┌──────────────────────────────┐
  Browser ─────▶│  app (Go/Gin, 单二进制)       │
                │  ├─ /            内嵌首页     │
                │  ├─ /api/v1/shorten  创建     │
                │  └─ /{code}        跳转       │
                └───────┬───────────┬──────────┘
                        │           │
                 ┌──────▼─────┐ ┌───▼──────────┐
                 │  MySQL 8   │ │  Redis 7     │
                 │ 链接持久化  │ │ 缓存 + 发号器 │
                 └────────────┘ └──────────────┘
```

三个服务即可运行；`nginx` 作为可选 profile 提供 80 端口反代与 gzip。

设计原则：

- **MySQL 是唯一真相源**，Redis 只做缓存与发号，丢了可恢复。
- **单二进制**：前端 `index.html` 通过 `go:embed` 打入，无需 Node 构建链。
- **两跳以内做完一次请求**：创建 = 1 次 Redis(`INCRBY`) + 1 次 MySQL(`INSERT`) + 1 次 Redis(`SETEX`)；跳转命中 = 1 次 Redis。

---

## 3. 关键技术决策

### 3.1 发号源：Redis INCRBY 号段预分配（已选定）

| 方案 | 评价 |
|---|---|
| **Redis INCRBY 取号段（选定）** | 每 1000 次发号才打一次 Redis，进程内无锁竞争（`sync.Mutex` 保护本地游标），性能最高 |
| 纯 Redis INCR | 每次创建都往返 Redis，实现最简单，但 QPS 上限被单 key 限制 |
| MySQL AUTO_INCREMENT | 需要 `INSERT` 拿 id 再 `UPDATE` 回填短码，写放大 2 倍，且短码生成与落库强耦合 |

实现：

```
Redis key: shortlink:idgen        类型 string，值 = 已分配到的最大序列号
每次申请：INCRBY shortlink:idgen 1000  → segEnd
本地号段：[segEnd-999, segEnd]，进程内原子递增消费
```

- 进程重启会丢弃号段剩余部分 → 只浪费号，不产生碰撞，无需回写 MySQL 校正。
- Redis 数据丢失是唯一风险：启动时执行 `SETNX shortlink:idgen <DB 中 MAX(seq)>`（DB 中无数据则为 `SEQ_START=100000`），保证不会从 0 重新发号导致撞号。Redis 同时开启 AOF。
- 多实例安全：每个实例通过 `INCRBY` 拿到互不重叠的区间。

### 3.2 短码生成：32 位 Feistel + Base62（定长 6 位）

不用哈希（无法保证唯一）、不用简单乘加（易被差分枚举），采用 **可逆双射**，因此**不需要查重**。

```
seq ──▶ mix32(seq) ──▶ (x + 2^32) mod 62^6 ──▶ base62 ──▶ 6 字符短码
```

- `mix32`：4 轮 Feistel 网络，两个 16 位半块，轮函数为 16 位整数混合（乘法 + xor-shift），4 个固定轮密钥。Feistel 结构保证对 32 位输入是**双射**，不同 seq 必得不同结果。
- 加偏移 `2^32` 再模 `62^6`（= 56,800,235,584）仍为双射，且把输出推到 `62^5` 之上 → **恒为 6 位**，短码长度整齐。
- 容量上限 2^32 ≈ 42.9 亿条；`seq >= 2^32` 时返回内部错误并告警（本期不可能触达）。
- 短码字符集：`0-9a-zA-Z`，对 URL 安全，大小写敏感。

示例（seq = 100000 → 某个固定结果，测试用例固化该映射，防止算法被误改）。

### 3.3 路由：短码走 `NoRoute` 兜底

Gin 的 radix 树中 `/{code}` 通配与 `/api/v1/shorten` 静态路径**冲突会 panic**。两个选择：

| 方案 | 评价 |
|---|---|
| **`GET /{code}` 用 `engine.NoRoute` 兜底（选定）** | 短链是最短形态 `http://host/aB3c9F`；`NoRoute` 中校验路径是否为 6 位 base62，是则跳转，否则 404 |
| 短码加前缀 `/s/{code}` | 零歧义，但短链变长、观感差 |

### 3.4 前端：`go:embed` 单页

`web/index.html` + `embed.FS`，用 `http.FileServer` 挂到 `/`。只有输入框、按钮、结果展示三段，原生 `fetch` 调 API，无依赖、无构建。

---

## 4. 数据模型

```sql
-- migrations/0001_init.sql
CREATE TABLE IF NOT EXISTS t_link (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    seq        BIGINT UNSIGNED NOT NULL COMMENT '发号器序列，用于重启后恢复 Redis 计数器',
    code       VARCHAR(16)     NOT NULL COMMENT '6 位 base62 短码',
    url        VARCHAR(2048)   NOT NULL COMMENT '原始长链接',
    created_at TIMESTAMP       NOT NULL DEFAULT CURRENT_TIMESTAMP,
    PRIMARY KEY (id),
    UNIQUE KEY uk_code (code),
    UNIQUE KEY uk_seq (seq)
) ENGINE = InnoDB DEFAULT CHARSET = utf8mb4 COLLATE = utf8mb4_0900_ai_ci;
```

- `uk_code` 是最终一致性保障：即使发号器异常重复，`INSERT` 也会失败并在应用层重新取号重试（最多 3 次）。
- `uk_seq` 让 `MAX(seq)` 恢复成为索引扫描，同时兜底重复号段。
- `url` 不建索引（长度 2048 超限，且本期无按 URL 查询需求）。
- 迁移由应用启动时执行内嵌 SQL（`CREATE TABLE IF NOT EXISTS`，幂等），不引入 migrate 工具。

---

## 5. 接口契约

统一错误体：

```json
{ "error": { "code": "INVALID_URL", "message": "url must be http or https" } }
```

### 5.1 创建短链

```
POST /api/v1/shorten
Content-Type: application/json

{ "url": "https://example.com/a/very/long/path?x=1" }
```

成功 `201`：

```json
{
  "code": "aB3c9F",
  "short_url": "http://localhost:8080/aB3c9F",
  "url": "https://example.com/a/very/long/path?x=1"
}
```

校验规则：

| 规则 | 失败响应 |
|---|---|
| 请求体 ≤ 4KB | `413 PAYLOAD_TOO_LARGE` |
| `url` 非空且 ≤ 2048 字符 | `400 INVALID_URL` |
| scheme 必须是 `http` / `https` | `400 INVALID_URL` |
| host 非空 | `400 INVALID_URL` |

不做"禁止跳转到自身"之类校验（YAGNI，短链跳短链无害）。

### 5.2 访问短链

```
GET /{code}
```

- 命中：`302 Found`，`Location: <原始 URL>`，`Cache-Control: no-store`（保证后续统计功能不受浏览器缓存干扰）。
- 未命中 / 非法短码：`404`，返回内嵌的极简 404 文本页（非关键路径也接受 JSON）。
- Redis 不可用：降级直查 MySQL，仍正常跳转。

### 5.3 健康检查

```
GET /healthz   → 200 {"status":"ok","mysql":"ok","redis":"ok"}   就绪探针，含依赖
GET /livez     → 200 {"status":"ok"}                            存活探针，不碰依赖
```

`/healthz` 任一依赖异常返回 `503`，供 compose `healthcheck` 使用；`/livez` 恒 200，供编排做 liveness，避免 Redis 抖动时重启一个仍在正常跳转的进程。

### 5.4 首页

```
GET /          → 200 text/html（内嵌 index.html）
```

---

## 6. 读写流程

### 创建

1. 校验 URL。
2. `idgen.Next()` 从本地号段取 `seq`（号段耗尽时 `INCRBY 1000`）。
3. `seq → mix32 → base62` 得 `code`。
4. `INSERT INTO t_link(seq, code, url)`；唯一键冲突则回到步骤 2（≤3 次）。
5. `SET link:{code} <url> EX 86400`（TTL 带 ±10% 抖动，防止同批过期造成缓存雪崩）。
6. 返回 `201`。

### 跳转

1. `GET link:{code}`。
2. 命中 → 302。
3. 未命中 → `SELECT url FROM t_link WHERE code = ?`
   - 查到 → `SETEX` 回填 → 302
   - 未查到 → `SET link:{code} "" EX 60`（空值缓存，防穿透）→ 404
4. Redis 调用出错（非 miss）→ 记 WARN 日志，直连 MySQL 读，不影响可用性。

缓存与 DB 的一致性：链接创建后不可变，**不存在更新路径**，因此无需处理失效——这是本期最小闭环带来的最大简化。

---

## 7. 项目结构

```
shortlink/
├── cmd/server/main.go              # 装配：config → logger → db → redis → idgen → router
├── internal/
│   ├── config/config.go            # 环境变量解析与校验
│   ├── httpapi/router.go           # Gin 路由 + NoRoute 兜底
│   ├── httpapi/link_handler.go     # 两个接口的入参校验与响应
│   ├── httpapi/middleware.go       # 请求日志、panic recover、CORS
│   ├── service/link.go             # 编排：取号 → 生成短码 → 落库 → 写缓存
│   ├── repo/link_repo.go           # MySQL 访问
│   ├── cache/link_cache.go         # Redis 缓存（Get/Set/空值）
│   ├── idgen/segment.go            # 号段发号器
│   ├── shortcode/feistel.go        # mix32 双射
│   ├── shortcode/base62.go         # 编解码
│   ├── store/mysql.go              # 连接 + 自动迁移
│   └── store/redis.go              # 连接
├── web/index.html                  # 内嵌单页
├── migrations/0001_init.sql
├── Dockerfile
├── docker-compose.yml
├── nginx/nginx.conf
├── go.mod
└── docs/plans/2026-10-29-shortlink-design.md
```

依赖：`gin-gonic/gin`、`go-sql-driver/mysql`、`redis/go-redis/v9`。不引入 ORM（两张表以内，`database/sql` 足够）。

配置项（环境变量）：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `HTTP_ADDR` | `:8080` | 监听地址 |
| `BASE_URL` | `http://localhost:8080` | 拼装 `short_url` 用 |
| `DB_DSN` | — | 必填 |
| `REDIS_ADDR` | `localhost:6379` | |
| `CACHE_TTL_SECONDS` | `86400` | 链接缓存 TTL（带 ±10% 抖动） |
| `SEGMENT_SIZE` | `1000` | 号段大小 |
| `SEQ_START` | `100000` | 空库时的起始序列 |

---

## 8. 部署：Docker Compose

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
      retries: 20

  app:
    build: .
    environment:
      DB_DSN: shortlink:shortlink@tcp(mysql:3306)/shortlink?parseTime=true&charset=utf8mb4
      REDIS_ADDR: redis:6379
      BASE_URL: http://localhost:8080
    ports: ["8080:8080"]
    depends_on:
      mysql: { condition: service_healthy }
      redis: { condition: service_healthy }

  nginx:                       # 可选：docker compose --profile proxy up
    image: nginx:1.27-alpine
    profiles: ["proxy"]
    ports: ["80:80"]
    volumes: ["./nginx/nginx.conf:/etc/nginx/conf.d/default.conf:ro"]
    depends_on: [app]

volumes:
  mysql-data:
  redis-data:
```

Dockerfile（多阶段，静态二进制 + 非 root）：

```dockerfile
FROM golang:1.24-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

FROM alpine:3.20
RUN adduser -D -u 10001 app
COPY --from=build /out/server /usr/local/bin/server
USER app
EXPOSE 8080
ENTRYPOINT ["server"]
```

启动顺序由 `depends_on` 的 `service_healthy` 保证；应用内部仍对 MySQL 连接做指数退避重试（最多 30s），避免容器编排竞态。表结构由应用启动时内嵌执行，不依赖 `docker-entrypoint-initdb.d`（避免只在空数据卷首启动时生效的坑）。

---

## 9. 错误处理与降级

| 场景 | 行为 |
|---|---|
| URL 非法 | `400`，不触碰任何依赖 |
| Redis 取号失败 | `503 SERVICE_UNAVAILABLE`（唯一性依赖发号，不可降级）。注意号段预分配的固有特性：号段缓冲未耗尽时创建仍可成功，耗尽后才 503 |
| MySQL 唯一键冲突 | 重新取号，最多 3 次，仍失败则 `500` |
| 跳转时 Redis 故障 | 降级直读 MySQL，正常 302 |
| 短码不存在 | 404 + 空值缓存 60s |
| panic | `recover` 中间件 → `500` + 完整堆栈进日志 |
| 号段耗尽 / seq 越界 | 500 + ERROR 日志告警 |

日志：`log/slog` 结构化输出到 stdout，字段含 `request_id`、`code`、`latency_ms`、`error`。

---

## 10. 测试策略

- **单元测试（无外部依赖）**
  - `shortcode`：双射验证（枚举 0..100万，断言输出无重复）、定长 6、字符集合法、固化 3 组 `seq → code` 快照。
  - `idgen`：mock Redis，验证号段边界只申请一次、并发 `Next()` 无重复（100 goroutine × 1000 次）。
  - URL 校验表驱动测试（合法/非法/CJK/超长/`javascript:` scheme）。
- **集成测试**：`testcontainers-go` 起 MySQL + Redis，跑完整 创建→跳转 链路，断言 302 的 `Location` 与缓存回填。
- **端到端**：`docker compose up` 后用 curl 脚本验证两条接口 + 并发创建 1000 条无重复短码。

---

## 11. 交付顺序

1. `go.mod` + 配置 + `store`（MySQL/Redis 连接与自动迁移）
2. `shortcode`（Feistel + Base62）+ 单元测试
3. `idgen` 号段 + 单元测试
4. `service` + `repo` + `cache` + `httpapi` 两个接口
5. `web/index.html` + `embed`
6. `Dockerfile` + `docker-compose.yml` + `nginx.conf`
7. 集成测试 + `README.md`（一条命令启动说明）

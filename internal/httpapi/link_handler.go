package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"shortlink/internal/service"
	"shortlink/internal/shortcode"
)

// maxBodyBytes 限制创建请求体大小。
const maxBodyBytes = 4 << 10

// sqlPinger 与 redisPinger 把健康检查与具体客户端解耦，便于测试替身。
type sqlPinger interface {
	PingContext(ctx context.Context) error
}

type redisPinger interface {
	Ping(ctx context.Context) *redis.StatusCmd
}

// LinkHandler 承载创建、跳转、健康检查三类请求。
type LinkHandler struct {
	svc *service.LinkService
	db  sqlPinger
	rdb redisPinger
}

func NewLinkHandler(svc *service.LinkService, db sqlPinger, rdb redisPinger) *LinkHandler {
	return &LinkHandler{svc: svc, db: db, rdb: rdb}
}

type createReq struct {
	URL string `json:"url"`
}

// Create 处理 POST /api/v1/shorten。
func (h *LinkHandler) Create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBodyBytes)

	var req createReq
	if err := c.ShouldBindJSON(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			abortWith(c, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "request body must be within 4KB")
			return
		}
		abortWith(c, http.StatusBadRequest, "INVALID_REQUEST", `request body must be a JSON object like {"url":"https://..."}`)
		return
	}

	u, err := validateURL(req.URL)
	if err != nil {
		abortWith(c, http.StatusBadRequest, "INVALID_URL", err.Error())
		return
	}

	link, err := h.svc.Create(c.Request.Context(), u)
	if errors.Is(err, service.ErrIDGenUnavailable) {
		// 发号失败不可降级：短码唯一性依赖发号器。
		abortWith(c, http.StatusServiceUnavailable, "IDGEN_UNAVAILABLE", "id generator unavailable, retry later")
		return
	}
	if err != nil {
		// 不记完整 URL：query 里常带 token / 邮箱等敏感信息，靠 rid 关联即可。
		slog.Error("create link failed", "url_len", len(u), "err", err)
		abortWith(c, http.StatusInternalServerError, "INTERNAL", "internal error")
		return
	}
	c.JSON(http.StatusCreated, link)
}

// Redirect 处理短码跳转。仅当路径是合法 6 位短码时才查询，其余一律 404。
func (h *LinkHandler) Redirect(c *gin.Context) {
	code := c.Param("code")
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
		slog.Error("resolve link failed", "code", code, "err", err)
		c.String(http.StatusInternalServerError, "internal error")
		return
	}

	// 不缓存 302，保证后续统计/失效能力不被浏览器缓存绕过。
	c.Header("Cache-Control", "no-store")
	c.Redirect(http.StatusFound, url)
}

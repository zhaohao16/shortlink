package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const healthTimeout = 2 * time.Second

// Livez 是存活探针：只证明进程还能处理请求，不碰任何依赖。
// 与 Healthz 分开，避免 Redis 故障时编排把仍在正常跳转的进程重启掉。
func (h *LinkHandler) Livez(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Healthz 是就绪探针：探测 MySQL 与 Redis，任一异常返回 503。
func (h *LinkHandler) Healthz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), healthTimeout)
	defer cancel()

	mysqlOK, redisOK := "ok", "ok"
	status := http.StatusOK

	if err := h.db.PingContext(ctx); err != nil {
		mysqlOK = "down"
		status = http.StatusServiceUnavailable
	}
	if err := h.rdb.Ping(ctx).Err(); err != nil {
		redisOK = "down"
		status = http.StatusServiceUnavailable
	}

	overall := "ok"
	if status != http.StatusOK {
		overall = "degraded"
	}
	c.JSON(status, gin.H{"status": overall, "mysql": mysqlOK, "redis": redisOK})
}

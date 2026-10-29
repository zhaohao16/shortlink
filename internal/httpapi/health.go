package httpapi

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

const healthTimeout = 2 * time.Second

// Healthz 探测 MySQL 与 Redis，任一异常返回 503，供容器编排就绪判断。
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

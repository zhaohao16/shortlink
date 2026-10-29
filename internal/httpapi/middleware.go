package httpapi

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// requestLogger 输出结构化访问日志，并把 request_id 回写响应头便于排障。
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		rid := c.GetHeader("X-Request-Id")
		if rid == "" {
			rid = start.Format("20060102150405.000000")
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

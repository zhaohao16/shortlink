package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

// maxRequestIDLen 限制外部传入的 X-Request-Id 长度，防止日志被灌入超长内容。
const maxRequestIDLen = 64

// validRequestID 只接受常见可见字符，其余丢弃并由服务端重新生成。
func validRequestID(s string) bool {
	if s == "" || len(s) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		ch := s[i]
		ok := (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= '0' && ch <= '9') || ch == '-' || ch == '_' || ch == '.' || ch == ':'
		if !ok {
			return false
		}
	}
	return true
}

// newRequestID 用"时间前缀 + 随机后缀"生成，避免纯时间戳在并发下不唯一。
func newRequestID(now time.Time) string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return now.UTC().Format("20060102T150405.000000")
	}
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b[:])
}

// requestLogger 输出结构化访问日志，并把 request_id 回写响应头便于排障。
func requestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		rid := c.GetHeader("X-Request-Id")
		if !validRequestID(rid) {
			rid = newRequestID(start)
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

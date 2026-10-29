package httpapi

import (
	"io/fs"

	"github.com/gin-gonic/gin"
)

// NewRouter 装配路由。
func NewRouter(h *LinkHandler, webFS fs.FS) *gin.Engine {
	r := gin.New()
	// 方法不匹配时返回 405 而不是落到 NoRoute，
	// 这样 GET /api/v1/shorten 与 GET /{code} 能被区分开。
	r.HandleMethodNotAllowed = true
	r.Use(gin.Recovery(), requestLogger())

	r.POST("/api/v1/shorten", h.Create)
	r.GET("/healthz", h.Healthz)
	r.GET("/", serveIndex(webFS))

	// 短码统一走 NoRoute：只有未被上面路由匹配的请求才会进入这里，
	// 因此短链能保持最短形态 /aB3c9F，同时避免 gin 中 /:code 通配
	// 与 /api/v1/... 静态路径冲突导致的 panic。
	r.NoRoute(shortcodeFallback(h))

	return r
}

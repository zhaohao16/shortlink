package httpapi

import (
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewRouter 装配路由。
func NewRouter(h *LinkHandler, webFS fs.FS) *gin.Engine {
	r := gin.New()
	// 方法不匹配时返回 405 而不是落到 NoRoute。
	// GET /api/v1/shorten（正确路径、错误方法）因此区别于 GET /abc（不存在的短码）。
	r.HandleMethodNotAllowed = true
	r.Use(gin.Recovery(), requestLogger())

	r.POST("/api/v1/shorten", h.Create)
	r.GET("/healthz", h.Healthz)
	r.GET("/livez", h.Livez)
	r.GET("/", serveIndex(webFS))
	// 浏览器会自动请求 favicon，落到 NoRoute 会白白产生 404 与一行日志。
	r.GET("/favicon.ico", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	// 短码统一走 NoRoute：只有未被上面路由匹配的请求才会进入这里，
	// 短链因此保持最短形态 /aB3c9F。
	//
	// 注：gin v1.10 其实允许 GET /:code 与 /api/v1/shorten 共存，注册时不会 panic
	// （静态路径优先匹配）。选 NoRoute 不是为了躲 panic，而是让路由表保持全静态：
	// 新增任何静态路由都不可能被通配段遮蔽，短码相关的格式校验与方法限制
	// 也集中在一处，不依赖"静态优先于通配"这一实现细节。
	r.NoRoute(shortcodeFallback(h))

	return r
}

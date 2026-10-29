package httpapi

import (
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

// NewRouter 装配路由。
func NewRouter(h *LinkHandler, webFS fs.FS) *gin.Engine {
	r := gin.New()
	// 方法不匹配时返回 405 而不是默认的 404。
	// GET /api/v1/shorten（正确路径、错误方法）因此区别于 GET /abc（不存在的短码）。
	r.HandleMethodNotAllowed = true
	r.Use(gin.Recovery(), requestLogger())

	r.POST("/api/v1/shorten", h.Create)
	r.GET("/healthz", h.Healthz)
	r.GET("/livez", h.Livez)
	r.GET("/", serveIndex(webFS))
	// 浏览器会自动请求 favicon，不注册会白白产生 404 与一行日志。
	r.GET("/favicon.ico", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	// 短码直连根路径，短链保持最短形态 /aB3c9F。
	// gin v1.10 允许 /:code 与上面的静态路径共存且静态优先（已由测试固化），
	// 所以 /healthz、/api/v1/shorten、/ 不会被通配吞掉；
	// 非 6 位 base62 的单段路径（如 /api）由 Redirect 内的格式校验挡成 404。
	r.GET("/:code", h.Redirect)
	r.HEAD("/:code", h.Redirect)

	return r
}

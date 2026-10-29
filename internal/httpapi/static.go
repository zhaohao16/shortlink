package httpapi

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/gin-gonic/gin"
)

// web 必须位于本包目录下：go:embed 只能嵌入当前包目录内的文件。
//
//go:embed web
var webFS embed.FS

// WebFS 返回内嵌前端资源，根为 web/ 目录。
func WebFS() fs.FS {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic("httpapi: embed web dir: " + err.Error())
	}
	return sub
}

func serveIndex(fsys fs.FS) gin.HandlerFunc {
	data, err := fs.ReadFile(fsys, "index.html")
	if err != nil {
		panic("httpapi: index.html missing from embedded FS: " + err.Error())
	}
	return func(c *gin.Context) {
		c.Data(http.StatusOK, "text/html; charset=utf-8", data)
	}
}

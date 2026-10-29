package httpapi

import "github.com/gin-gonic/gin"

// errBody 是统一错误响应体：{"error":{"code":"...","message":"..."}}。
type errBody struct {
	Error errDetail `json:"error"`
}

type errDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func abortWith(c *gin.Context, status int, code, msg string) {
	c.AbortWithStatusJSON(status, errBody{Error: errDetail{Code: code, Message: msg}})
}

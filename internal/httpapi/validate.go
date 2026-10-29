package httpapi

import (
	"errors"
	"net/url"
)

var errInvalidURL = errors.New("url must be a valid http/https address within 2048 characters")

// maxURLLen 与 t_link.url 列宽保持一致。
const maxURLLen = 2048

// validateURL 只接受 http/https 绝对地址，挡掉 javascript:/file: 等危险 scheme。
func validateURL(raw string) (string, error) {
	if raw == "" || len(raw) > maxURLLen {
		return "", errInvalidURL
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errInvalidURL
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", errInvalidURL
	}
	// 用 Hostname 而非 Host：http://:8080/ 的 Host 非空但主机名是空的。
	if u.Hostname() == "" {
		return "", errInvalidURL
	}
	return u.String(), nil
}

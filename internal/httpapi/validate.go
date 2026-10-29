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
	if u.Host == "" {
		return "", errInvalidURL
	}
	return u.String(), nil
}

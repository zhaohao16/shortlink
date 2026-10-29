package httpapi

import (
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		wantErr bool
	}{
		{"简单 https", "https://example.com", false},
		{"带路径查询", "https://example.com/a/b?c=1&d=2", false},
		{"http", "http://example.com", false},
		{"带端口", "http://127.0.0.1:8080/a", false},
		{"空字符串", "", true},
		{"缺 scheme", "example.com", true},
		{"危险 scheme", "javascript:alert(1)", true},
		{"file scheme", "file:///etc/passwd", true},
		{"无 host", "https://", true},
		{"空 hostname", "http://:8080/", true},
		{"只有 userinfo", "http://user@:8080/", true},
		{"超长", "https://example.com/" + strings.Repeat("a", 2100), true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := validateURL(c.in)
			if (err != nil) != c.wantErr {
				t.Fatalf("validateURL(%q) err = %v, wantErr %v", c.in, err, c.wantErr)
			}
		})
	}
}

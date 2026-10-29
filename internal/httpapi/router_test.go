package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"shortlink/internal/cache"
	"shortlink/internal/idgen"
	"shortlink/internal/repo"
	"shortlink/internal/service"
)

func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

// memStore 用内存 map 代替 MySQL。
type memStore struct{ rows map[string]string }

func (m *memStore) Insert(ctx context.Context, seq uint64, code, url string) error {
	m.rows[code] = url
	return nil
}

func (m *memStore) FindURLByCode(ctx context.Context, code string) (string, error) {
	if u, ok := m.rows[code]; ok {
		return u, nil
	}
	return "", repo.ErrNotFound
}

type okDB struct{ down bool }

func (o okDB) PingContext(ctx context.Context) error {
	if o.down {
		return context.DeadlineExceeded
	}
	return nil
}

func newTestHandler(t *testing.T) (*LinkHandler, *memStore) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := idgen.New(context.Background(), rdb, "test:idgen", 10, 100000)
	if err != nil {
		t.Fatalf("idgen: %v", err)
	}
	ms := &memStore{rows: map[string]string{}}
	svc := service.NewLinkService(ms, cache.NewLinkCache(rdb, time.Hour), g, "http://localhost:8080")
	return NewLinkHandler(svc, okDB{}, rdb), ms
}

func testRouter(t *testing.T) (*gin.Engine, *memStore) {
	t.Helper()
	h, ms := newTestHandler(t)
	fsys := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("<html>ok</html>")}}
	return NewRouter(h, fsys), ms
}

func postJSON(r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func extractCode(t *testing.T, body []byte) string {
	t.Helper()
	var resp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal %s: %v", body, err)
	}
	return resp.Code
}

func TestCreateEndpoint(t *testing.T) {
	r, _ := testRouter(t)

	w := postJSON(r, "/api/v1/shorten", `{"url":"https://example.com/a"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	var resp service.Link
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Code) != 6 {
		t.Fatalf("code = %q", resp.Code)
	}
	if resp.ShortURL != "http://localhost:8080/"+resp.Code {
		t.Fatalf("short_url = %q", resp.ShortURL)
	}
	if resp.URL != "https://example.com/a" {
		t.Fatalf("url = %q", resp.URL)
	}
}

func TestCreateRejectsBadInput(t *testing.T) {
	r, _ := testRouter(t)

	tests := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{"非法 scheme", `{"url":"javascript:alert(1)"}`, http.StatusBadRequest, "INVALID_URL"},
		{"缺 url 字段", `{}`, http.StatusBadRequest, "INVALID_URL"},
		{"类型错误", `{"url":123}`, http.StatusBadRequest, "INVALID_REQUEST"},
		{"不是 JSON", `not-json`, http.StatusBadRequest, "INVALID_REQUEST"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := postJSON(r, "/api/v1/shorten", tc.body)
			if w.Code != tc.wantCode {
				t.Fatalf("status = %d, want %d", w.Code, tc.wantCode)
			}
			if !bytes.Contains(w.Body.Bytes(), []byte(tc.wantErr)) {
				t.Fatalf("body %s does not contain %q", w.Body, tc.wantErr)
			}
		})
	}
}

func TestCreateRejectsOversizedBody(t *testing.T) {
	r, _ := testRouter(t)
	big := `{"url":"https://example.com/` + string(bytes.Repeat([]byte("a"), 5000)) + `"}`
	w := postJSON(r, "/api/v1/shorten", big)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", w.Code)
	}
}

func TestRedirectAndRouteConflicts(t *testing.T) {
	r, ms := testRouter(t)

	w := postJSON(r, "/api/v1/shorten", `{"url":"https://example.com/target"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status %d", w.Code)
	}
	code := extractCode(t, w.Body.Bytes())
	if ms.rows[code] != "https://example.com/target" {
		t.Fatal("link not stored")
	}

	tests := []struct {
		name     string
		method   string
		path     string
		wantCode int
		wantLoc  string
	}{
		{"跳转命中", http.MethodGet, "/" + code, http.StatusFound, "https://example.com/target"},
		{"HEAD 也支持", http.MethodHead, "/" + code, http.StatusFound, "https://example.com/target"},
		{"长度不足", http.MethodGet, "/abc", http.StatusNotFound, ""},
		{"含非法字符", http.MethodGet, "/abc-12", http.StatusNotFound, ""},
		{"不存在的短码", http.MethodGet, "/000000", http.StatusNotFound, ""},
		{"API 不被短码吞掉", http.MethodGet, "/api/v1/shorten", http.StatusMethodNotAllowed, ""},
		{"非 GET 打短码路径", http.MethodPost, "/" + code, http.StatusNotFound, ""},
		{"健康检查不被吞掉", http.MethodGet, "/healthz", http.StatusOK, ""},
		{"存活探针", http.MethodGet, "/livez", http.StatusOK, ""},
		{"favicon 不产生 404", http.MethodGet, "/favicon.ico", http.StatusNoContent, ""},
		{"首页", http.MethodGet, "/", http.StatusOK, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.wantCode {
				t.Fatalf("%s %s status = %d, want %d", tc.method, tc.path, w.Code, tc.wantCode)
			}
			if tc.wantLoc != "" && w.Header().Get("Location") != tc.wantLoc {
				t.Fatalf("Location = %q, want %q", w.Header().Get("Location"), tc.wantLoc)
			}
		})
	}
}

func TestRedirectSetsNoStore(t *testing.T) {
	r, ms := testRouter(t)
	ms.rows["abc123"] = "https://example.com/n"
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/abc123", nil))
	if w.Code != http.StatusFound {
		t.Fatalf("status = %d", w.Code)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Fatalf("Cache-Control = %q", cc)
	}
}

func TestHealthz(t *testing.T) {
	h, _ := newTestHandler(t)
	r := NewRouter(h, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"status":"ok"`)) {
		t.Fatalf("body = %s", w.Body)
	}
}

func TestHealthzDegraded(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	ms := &memStore{rows: map[string]string{}}
	svc := service.NewLinkService(ms, cache.NewLinkCache(rdb, time.Hour), nil, "http://x")
	h := NewLinkHandler(svc, okDB{down: true}, rdb)
	r := NewRouter(h, fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"mysql":"down"`)) {
		t.Fatalf("body = %s", w.Body)
	}
}

func TestRequestIDHeader(t *testing.T) {
	r, _ := testRouter(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("X-Request-Id", "abc-123")
	r.ServeHTTP(w, req)
	if got := w.Header().Get("X-Request-Id"); got != "abc-123" {
		t.Fatalf("X-Request-Id = %q", got)
	}
}

// TestRequestIDSanitized 验证外部传入的非法/超长 request id 会被替换，避免日志被灌爆。
func TestRequestIDSanitized(t *testing.T) {
	r, _ := testRouter(t)
	for _, bad := range []string{"has space", strings.Repeat("a", 200), "中文 id"} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
		req.Header.Set("X-Request-Id", bad)
		r.ServeHTTP(w, req)
		got := w.Header().Get("X-Request-Id")
		if got == bad || got == "" {
			t.Fatalf("X-Request-Id %q not sanitized (got %q)", bad, got)
		}
	}
}

// flakyStore 前 N 次 Insert 强制返回 ErrDuplicate，用于覆盖路由层的重试兜底。
type flakyStore struct {
	*memStore
	failFirst int
}

func (f *flakyStore) Insert(ctx context.Context, seq uint64, code, url string) error {
	if f.failFirst > 0 {
		f.failFirst--
		return repo.ErrDuplicate
	}
	return f.memStore.Insert(ctx, seq, code, url)
}

// TestCreateRetriesOnDuplicateAtRouteLayer 是唯一性保障的最后一环：
// 撞码必须重新取号重试并最终返回 201，而不是 500。
func TestCreateRetriesOnDuplicateAtRouteLayer(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := idgen.New(context.Background(), rdb, "flaky:idgen", 10, 100000)
	if err != nil {
		t.Fatal(err)
	}
	st := &flakyStore{memStore: &memStore{rows: map[string]string{}}, failFirst: 2}
	svc := service.NewLinkService(st, cache.NewLinkCache(rdb, time.Hour), g, "http://localhost:8080")
	r := NewRouter(NewLinkHandler(svc, okDB{}, rdb),
		fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("ok")}})

	w := postJSON(r, "/api/v1/shorten", `{"url":"https://example.com/retry"}`)
	if w.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", w.Code, w.Body)
	}
}

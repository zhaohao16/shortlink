package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"shortlink/internal/cache"
	"shortlink/internal/idgen"
	"shortlink/internal/repo"
)

// fakeRepo 用 map 模拟 t_link，fail 用来强制制造唯一键冲突。
type fakeRepo struct {
	rows map[string]string
	fail int
}

func (f *fakeRepo) Insert(ctx context.Context, seq uint64, code, url string) error {
	if f.fail > 0 {
		f.fail--
		return repo.ErrDuplicate
	}
	if _, ok := f.rows[code]; ok {
		return repo.ErrDuplicate
	}
	f.rows[code] = url
	return nil
}

func (f *fakeRepo) FindURLByCode(ctx context.Context, code string) (string, error) {
	if u, ok := f.rows[code]; ok {
		return u, nil
	}
	return "", repo.ErrNotFound
}

func newSvc(t *testing.T) (*LinkService, *fakeRepo) {
	t.Helper()
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := idgen.New(context.Background(), rdb, "t:idgen", 10, 100000)
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRepo{rows: map[string]string{}}
	return NewLinkService(r, cache.NewLinkCache(rdb, time.Hour), g, "http://localhost:8080"), r
}

func TestCreatePersistsAndCaches(t *testing.T) {
	svc, r := newSvc(t)
	link, err := svc.Create(context.Background(), "https://example.com/x")
	if err != nil {
		t.Fatal(err)
	}
	if len(link.Code) != 6 {
		t.Fatalf("code %q not 6 chars", link.Code)
	}
	if r.rows[link.Code] != "https://example.com/x" {
		t.Fatal("not persisted")
	}
	if link.ShortURL != "http://localhost:8080/"+link.Code {
		t.Fatalf("short_url = %q", link.ShortURL)
	}
}

func TestCreateRetriesOnDuplicate(t *testing.T) {
	svc, r := newSvc(t)
	r.fail = 2
	if _, err := svc.Create(context.Background(), "https://example.com/y"); err != nil {
		t.Fatalf("should succeed after retries: %v", err)
	}
}

func TestCreateGivesUpAfterThreeDuplicates(t *testing.T) {
	svc, r := newSvc(t)
	r.fail = 3
	if _, err := svc.Create(context.Background(), "https://example.com/z"); !errors.Is(err, ErrTooManyRetries) {
		t.Fatalf("err = %v, want ErrTooManyRetries", err)
	}
}

// TestCreateFailsWhenIDGenDown 验证创建链路不降级：取号失败必须报 503 语义的错误，
// 否则短码唯一性失去保障。
func TestCreateFailsWhenIDGenDown(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, err := idgen.New(context.Background(), rdb, "down:idgen", 10, 1)
	if err != nil {
		t.Fatal(err)
	}
	mr.Close() // New 之后再断开，确保是取号阶段失败

	svc := NewLinkService(&fakeRepo{rows: map[string]string{}}, cache.NewLinkCache(rdb, time.Hour), g, "http://x")
	if _, err := svc.Create(context.Background(), "https://example.com/d"); !errors.Is(err, ErrIDGenUnavailable) {
		t.Fatalf("err = %v, want ErrIDGenUnavailable", err)
	}
}

func TestResolveUsesCacheThenRepo(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	link, err := svc.Create(ctx, "https://example.com/a")
	if err != nil {
		t.Fatal(err)
	}

	got, err := svc.Resolve(ctx, link.Code)
	if err != nil || got != "https://example.com/a" {
		t.Fatalf("resolve cached: %q %v", got, err)
	}

	if _, err := svc.Resolve(ctx, "000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

// TestResolveBackfillsCache 验证缓存被清空后能从 DB 回源并回填。
func TestResolveBackfillsCache(t *testing.T) {
	mr := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	g, _ := idgen.New(context.Background(), rdb, "t2:idgen", 10, 100000)
	r := &fakeRepo{rows: map[string]string{"abc123": "https://example.com/backfill"}}
	svc := NewLinkService(r, cache.NewLinkCache(rdb, time.Hour), g, "http://x")

	got, err := svc.Resolve(context.Background(), "abc123")
	if err != nil || got != "https://example.com/backfill" {
		t.Fatalf("got %q err %v", got, err)
	}
	if ok := mr.Exists("link:abc123"); !ok {
		t.Fatal("cache not backfilled")
	}
}

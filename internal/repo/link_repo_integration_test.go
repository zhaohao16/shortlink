//go:build integration

// 集成测试：需要真实 MySQL，默认不参与 `go test ./...`。
//
//	docker compose up -d mysql redis
//	TEST_MYSQL_DSN='shortlink:shortlink@tcp(mysql:3306)/shortlink?parseTime=true&charset=utf8mb4' \
//	  go test -tags integration ./internal/repo/ -v
//
// 注意：会清空 t_link，务必指向一次性数据库。
package repo

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"

	"shortlink/internal/store"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN not set")
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatalf("open mysql: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatalf("ping mysql: %v", err)
	}
	if err := store.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := db.ExecContext(ctx, "DELETE FROM t_link"); err != nil {
		t.Fatalf("clean table: %v", err)
	}
	return db
}

func TestInsertAndFind(t *testing.T) {
	r := NewLinkRepo(openTestDB(t))
	ctx := context.Background()

	if err := r.Insert(ctx, 100000, "aB3c9F", "https://example.com/x"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	got, err := r.FindURLByCode(ctx, "aB3c9F")
	if err != nil || got != "https://example.com/x" {
		t.Fatalf("find = %q, %v", got, err)
	}
}

func TestFindMissingReturnsErrNotFound(t *testing.T) {
	r := NewLinkRepo(openTestDB(t))
	if _, err := r.FindURLByCode(context.Background(), "000000"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestDuplicateCodeMapsToErrDuplicate 验证 MySQL 1062 被正确归一为 ErrDuplicate，
// 这是 service 层"撞码重试"的唯一信号来源。
func TestDuplicateCodeMapsToErrDuplicate(t *testing.T) {
	r := NewLinkRepo(openTestDB(t))
	ctx := context.Background()

	if err := r.Insert(ctx, 1, "dupdup", "https://example.com/a"); err != nil {
		t.Fatal(err)
	}
	err := r.Insert(ctx, 2, "dupdup", "https://example.com/b")
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestDuplicateSeqMapsToErrDuplicate(t *testing.T) {
	r := NewLinkRepo(openTestDB(t))
	ctx := context.Background()

	if err := r.Insert(ctx, 7, "aaaaaa", "https://example.com/a"); err != nil {
		t.Fatal(err)
	}
	err := r.Insert(ctx, 7, "bbbbbb", "https://example.com/b")
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("err = %v, want ErrDuplicate", err)
	}
}

func TestMaxSeq(t *testing.T) {
	db := openTestDB(t)
	r := NewLinkRepo(db)
	ctx := context.Background()

	if v, err := store.MaxSeq(ctx, db); err != nil || v != 0 {
		t.Fatalf("empty max seq = %d, %v want 0", v, err)
	}
	if err := r.Insert(ctx, 42, "cccccc", "https://example.com/c"); err != nil {
		t.Fatal(err)
	}
	if err := r.Insert(ctx, 4242, "dddddd", "https://example.com/d"); err != nil {
		t.Fatal(err)
	}
	if v, err := store.MaxSeq(ctx, db); err != nil || v != 4242 {
		t.Fatalf("max seq = %d, %v want 4242", v, err)
	}
}

// TestLongURLRoundTrip 确认 2048 字节的 URL 能完整存取（utf8mb4 下中文按字符计）。
func TestLongURLRoundTrip(t *testing.T) {
	r := NewLinkRepo(openTestDB(t))
	ctx := context.Background()

	long := "https://example.com/" + strings.Repeat("路径", 600) // 远超 2048 字节
	if err := r.Insert(ctx, 9, "eeeeee", long); err != nil {
		t.Fatalf("insert long url: %v", err)
	}
	got, err := r.FindURLByCode(ctx, "eeeeee")
	if err != nil || got != long {
		t.Fatalf("long url mismatch: err=%v len=%d want=%d", err, len(got), len(long))
	}
}

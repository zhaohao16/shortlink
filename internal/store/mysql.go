package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sort"
	"time"

	_ "github.com/go-sql-driver/mysql"
)

// migrations 必须位于本包目录下：go:embed 只能嵌入当前包目录内的文件。
//
//go:embed migrations/*.sql
var migrationFS embed.FS

const (
	mysqlPingAttempts = 15
	mysqlPingInterval = time.Second
)

// OpenMySQL 建立连接池，并在启动期做有限次重试，避免容器编排竞态。
func OpenMySQL(ctx context.Context, dsn string) (*sql.DB, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(50)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)
	db.SetConnMaxIdleTime(5 * time.Minute)

	var lastErr error
	for i := 0; i < mysqlPingAttempts; i++ {
		pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		lastErr = db.PingContext(pingCtx)
		cancel()
		if lastErr == nil {
			return db, nil
		}
		slog.Warn("mysql not ready, retrying", "attempt", i+1, "err", lastErr)
		select {
		case <-ctx.Done():
			_ = db.Close()
			return nil, ctx.Err()
		case <-time.After(mysqlPingInterval):
		}
	}
	_ = db.Close()
	return nil, fmt.Errorf("mysql not reachable: %w", lastErr)
}

// Migrate 按文件名顺序执行全部内嵌迁移。
// 不做 schema_migrations 版本表（本期只有建表），因此每个迁移文件都必须自身幂等，
// 例如 CREATE TABLE IF NOT EXISTS / ALTER TABLE ... ADD COLUMN IF NOT EXISTS。
func Migrate(ctx context.Context, db *sql.DB) error {
	files, err := fs.Glob(migrationFS, "migrations/*.sql")
	if err != nil {
		return fmt.Errorf("glob migrations: %w", err)
	}
	if len(files) == 0 {
		return errors.New("store: no migration files embedded")
	}
	sort.Strings(files)

	for _, name := range files {
		body, err := migrationFS.ReadFile(name)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if _, err := db.ExecContext(ctx, string(body)); err != nil {
			return fmt.Errorf("apply %s: %w", name, err)
		}
		slog.Info("migration applied", "file", name)
	}
	return nil
}

// MaxSeq 返回库中已使用的最大序列号，用作发号器的兜底起点。
func MaxSeq(ctx context.Context, db *sql.DB) (uint64, error) {
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, "SELECT MAX(seq) FROM t_link").Scan(&v); err != nil {
		return 0, fmt.Errorf("query max seq: %w", err)
	}
	if !v.Valid || v.Int64 < 0 {
		return 0, nil
	}
	return uint64(v.Int64), nil
}

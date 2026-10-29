package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"log/slog"
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

// Migrate 执行幂等建表语句。表结构只增不改，变更必须新增迁移文件。
func Migrate(ctx context.Context, db *sql.DB) error {
	body, err := migrationFS.ReadFile("migrations/0001_init.sql")
	if err != nil {
		return fmt.Errorf("read migration: %w", err)
	}
	if _, err := db.ExecContext(ctx, string(body)); err != nil {
		return fmt.Errorf("apply migration: %w", err)
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

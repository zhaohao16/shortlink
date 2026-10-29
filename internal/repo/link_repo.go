package repo

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/go-sql-driver/mysql"
)

// errDuplicateCode 是 MySQL 唯一键冲突的错误码。
const errDuplicateCode = 1062

var (
	// ErrDuplicate 表示 seq 或 code 撞上了唯一键，调用方应重新取号。
	ErrDuplicate = errors.New("repo: duplicate code")
	// ErrNotFound 表示短码不存在。
	ErrNotFound = errors.New("repo: link not found")
)

// LinkRepo 是 t_link 的唯一访问入口。
type LinkRepo struct{ db *sql.DB }

func NewLinkRepo(db *sql.DB) *LinkRepo { return &LinkRepo{db: db} }

// Insert 写入一条链接记录。唯一键冲突归一为 ErrDuplicate。
func (r *LinkRepo) Insert(ctx context.Context, seq uint64, code, url string) error {
	_, err := r.db.ExecContext(ctx,
		"INSERT INTO t_link (seq, code, url) VALUES (?, ?, ?)", seq, code, url)
	if err == nil {
		return nil
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == errDuplicateCode {
		return ErrDuplicate
	}
	return fmt.Errorf("insert link: %w", err)
}

// FindURLByCode 按短码查原始 URL。
func (r *LinkRepo) FindURLByCode(ctx context.Context, code string) (string, error) {
	var url string
	err := r.db.QueryRowContext(ctx, "SELECT url FROM t_link WHERE code = ?", code).Scan(&url)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("query link: %w", err)
	}
	return url, nil
}

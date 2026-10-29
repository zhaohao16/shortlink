package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"shortlink/internal/cache"
	"shortlink/internal/idgen"
	"shortlink/internal/repo"
	"shortlink/internal/shortcode"
)

var (
	// ErrNotFound 表示短码不存在。
	ErrNotFound = errors.New("service: link not found")
	// ErrTooManyRetries 表示连续撞码，发号或数据异常。
	ErrTooManyRetries = errors.New("service: too many duplicate retries")
	// ErrIDGenUnavailable 表示取号失败，创建接口必须失败（唯一性依赖发号）。
	ErrIDGenUnavailable = errors.New("service: id generator unavailable")
)

// maxInsertRetry 是唯一键冲突后的最大尝试次数。
const maxInsertRetry = 3

// LinkStore 是仓储的最小接口，便于测试替身。
type LinkStore interface {
	Insert(ctx context.Context, seq uint64, code, url string) error
	FindURLByCode(ctx context.Context, code string) (string, error)
}

// Link 是创建短链的返回值。
type Link struct {
	Code     string `json:"code"`
	ShortURL string `json:"short_url"`
	URL      string `json:"url"`
}

// LinkService 编排取号、生成短码、落库与缓存。
type LinkService struct {
	repo    LinkStore
	cache   *cache.LinkCache
	idgen   *idgen.Generator
	baseURL string
}

func NewLinkService(r LinkStore, c *cache.LinkCache, g *idgen.Generator, baseURL string) *LinkService {
	return &LinkService{repo: r, cache: c, idgen: g, baseURL: baseURL}
}

// Create 生成短链。发号 → 编码 → 落库，撞唯一键则重新取号。
func (s *LinkService) Create(ctx context.Context, rawURL string) (*Link, error) {
	for attempt := 0; attempt < maxInsertRetry; attempt++ {
		seq, err := s.idgen.Next(ctx)
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrIDGenUnavailable, err)
		}
		code, err := shortcode.Encode(seq)
		if err != nil {
			return nil, err
		}
		err = s.repo.Insert(ctx, seq, code, rawURL)
		if err == nil {
			// 缓存不是强依赖：写失败只告警，不影响创建结果。
			if cerr := s.cache.Set(ctx, code, rawURL); cerr != nil {
				slog.Warn("cache set failed", "code", code, "err", cerr)
			}
			return &Link{Code: code, ShortURL: s.baseURL + "/" + code, URL: rawURL}, nil
		}
		if !errors.Is(err, repo.ErrDuplicate) {
			return nil, err
		}
		slog.Warn("duplicate code, retrying", "code", code, "attempt", attempt+1)
	}
	return nil, ErrTooManyRetries
}

// Resolve 返回短码对应的原始 URL。
// 缓存故障时降级直读 DB（可用性优先）；查不到则写空值缓存防穿透。
func (s *LinkService) Resolve(ctx context.Context, code string) (string, error) {
	url, found, err := s.cache.Get(ctx, code)
	switch {
	case err != nil:
		slog.Warn("cache get failed, fallback to db", "code", code, "err", err)
	case found:
		if url == "" { // 空值缓存
			return "", ErrNotFound
		}
		return url, nil
	}

	url, err = s.repo.FindURLByCode(ctx, code)
	if errors.Is(err, repo.ErrNotFound) {
		if nerr := s.cache.SetNegative(ctx, code); nerr != nil {
			slog.Warn("negative cache failed", "code", code, "err", nerr)
		}
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	if serr := s.cache.Set(ctx, code, url); serr != nil {
		slog.Warn("cache backfill failed", "code", code, "err", serr)
	}
	return url, nil
}

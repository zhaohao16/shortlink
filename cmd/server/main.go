package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"shortlink/internal/cache"
	"shortlink/internal/config"
	"shortlink/internal/httpapi"
	"shortlink/internal/idgen"
	"shortlink/internal/repo"
	"shortlink/internal/service"
	"shortlink/internal/store"
)

// idgenKey 是 Redis 中发号计数器的键名。
const idgenKey = "shortlink:idgen"

func main() {
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	bootCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	db, err := store.OpenMySQL(bootCtx, cfg.DBDSN)
	if err != nil {
		return err
	}
	defer db.Close()

	if err := store.Migrate(bootCtx, db); err != nil {
		return err
	}

	rdb, err := store.OpenRedis(bootCtx, cfg.RedisAddr)
	if err != nil {
		return err
	}
	defer rdb.Close()

	// 用库中 MAX(seq) 兜底：Redis 数据丢失时不会从 0 重发导致撞号。
	maxSeq, err := store.MaxSeq(bootCtx, db)
	if err != nil {
		return err
	}
	start := cfg.SeqStart
	if maxSeq >= start {
		start = maxSeq + 1
	}
	gen, err := idgen.New(bootCtx, rdb, idgenKey, cfg.SegmentSize, start)
	if err != nil {
		return err
	}
	slog.Info("idgen ready", "start", start, "segment", cfg.SegmentSize, "db_max_seq", maxSeq)

	svc := service.NewLinkService(repo.NewLinkRepo(db), cache.NewLinkCache(rdb, cfg.CacheTTL), gen, cfg.BaseURL)
	srv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: httpapi.NewRouter(httpapi.NewLinkHandler(svc, db, rdb), httpapi.WebFS()),
		// ReadTimeout 限制整个请求的读取（含 body），防 Slowloris；请求体上限 4KB，10s 充裕。
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		slog.Info("server started", "addr", cfg.HTTPAddr, "base_url", cfg.BaseURL)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancelShutdown()
	slog.Info("shutting down")

	// 排空超时是可控的运维事件，不该伪装成崩溃（退出码 1）。
	if err := srv.Shutdown(shutdownCtx); err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			slog.Warn("graceful shutdown timed out, closing forcefully")
			return nil
		}
		return err
	}
	return nil
}

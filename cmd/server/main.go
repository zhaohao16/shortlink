package main

import (
	"log/slog"
	"os"

	"shortlink/internal/config"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("load config", "err", err)
		os.Exit(1)
	}
	slog.Info("boot", "addr", cfg.HTTPAddr, "base_url", cfg.BaseURL)
}

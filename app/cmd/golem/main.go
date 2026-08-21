package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/ishi-o/golem/app"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/storage"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "err", err)
		os.Exit(1)
	}
	store := storage.NewFileSystem(cfg.Storage.Location)
	if err := store.Init(); err != nil {
		logger.Error("initialize storage", "err", err)
		os.Exit(1)
	}
	router := app.NewRouter(app.RouterConfig{Logger: logger, Ready: func(context.Context) error { return nil }})
	server := app.NewServer(envOr("GOLEM_HTTP_ADDR", ":8080"), router, logger)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("golem app listening", "address", server.HTTP.Addr, "config", cfg.String())
	if err := server.Run(ctx); err != nil && err != http.ErrServerClosed {
		logger.Error("HTTP server stopped", "err", err)
		os.Exit(1)
	}
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

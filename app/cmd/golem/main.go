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

	"github.com/ishi-o/golem/app"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/internal/bootstrap"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := bootstrap.Load()
	if err != nil {
		logger.Error("load configuration", "err", err)
		os.Exit(1)
	}
	store := storage.NewFileSystem(cfg.Storage.Location)
	if err := store.Init(); err != nil {
		logger.Error("initialize storage", "err", err)
		os.Exit(1)
	}
	runtime, err := bootstrap.New(context.Background(), cfg, logger)
	if err != nil {
		logger.Error("bootstrap application runtime", "err", err, "config", cfg.String())
		os.Exit(1)
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := runtime.Close(closeContext); err != nil {
			logger.Error("close application runtime", "err", err)
		}
	}()

	router := app.NewRouter(app.RouterConfig{
		Logger: logger,
		Agent:  runtime.Agent,
		Ready: func(context.Context) error {
			if !runtime.Agent.Accepting() {
				return errors.New("agent is shutting down")
			}
			return nil
		},
	})
	server := app.NewServer(envOr("GOLEM_HTTP_ADDR", ":8080"), router)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger.Info("golem app listening", "address", server.Addr(), "config", cfg.String())
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

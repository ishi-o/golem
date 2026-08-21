package main

import (
	"context"
	"log/slog"
	"os"

	"github.com/ishi-o/golem/cmd"
	"github.com/ishi-o/golem/core/config"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "err", err)
		os.Exit(1)
	}
	// Model construction is intentionally injected by an embedding binary.
	// The command still exposes help, version, and a precise configuration
	// error when it is run without that dependency setup.
	root := cmd.NewRoot(cmd.Config{Input: os.Stdin, Output: os.Stdout, Logger: logger, UserID: "local", Session: "local"})
	if err := root.ExecuteContext(context.Background()); err != nil {
		logger.Error("command failed", "err", err, "config", cfg.String())
		os.Exit(1)
	}
}

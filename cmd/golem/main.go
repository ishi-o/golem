package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/ishi-o/golem/cmd"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/internal/bootstrap"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg, err := config.Load()
	if err != nil {
		logger.Error("load configuration", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	var runtime *bootstrap.Runtime
	if commandNeedsRuntime(os.Args[1:]) {
		runtime, err = bootstrap.New(ctx, cfg, logger)
		if err != nil {
			logger.Error("bootstrap command runtime", "err", err, "config", cfg.String())
			os.Exit(1)
		}
		defer func() {
			if err := runtime.Close(ctx); err != nil {
				logger.Error("close command runtime", "err", err)
			}
		}()
	}

	var runner cmd.Runner
	if runtime != nil {
		runner = runtime.Agent
	}
	root := cmd.NewRoot(cmd.Config{
		Runner:  runner,
		Input:   os.Stdin,
		Output:  os.Stdout,
		Logger:  logger,
		UserID:  "local",
		Session: "local",
	})
	if err := root.ExecuteContext(context.Background()); err != nil {
		logger.Error("command failed", "err", err, "config", cfg.String())
		os.Exit(1)
	}
}

func commandNeedsRuntime(args []string) bool {
	if len(args) == 0 {
		return false
	}
	for _, arg := range args {
		if arg == "--help" || arg == "-h" || arg == "help" || arg == "version" {
			return false
		}
	}
	return strings.TrimSpace(args[0]) == "chat" || strings.TrimSpace(args[0]) == "cancel"
}

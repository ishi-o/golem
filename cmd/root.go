// Package cmd builds the golem command line with Cobra. The command package
// owns terminal interaction only; callers inject the core runner so the CLI
// does not choose a model provider or persistence backend on their behalf.
package cmd

import (
	"io"
	"log/slog"
	"strings"

	"github.com/ishi-o/golem/core/agent"
	"github.com/spf13/cobra"
)

// Runner is the small portion of the core runtime the CLI needs.
type Runner interface {
	Fire(agent.Request) error
	Cancel(requestID string) bool
}

// Config configures the command tree.
type Config struct {
	Runner  Runner
	UserID  string
	Session string
	Input   io.Reader
	Output  io.Writer
	Logger  *slog.Logger
}

// NewRoot creates the root command and its subcommands.
func NewRoot(config Config) *cobra.Command {
	if config.Input == nil {
		config.Input = strings.NewReader("")
	}
	if config.Output == nil {
		config.Output = io.Discard
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.UserID == "" {
		config.UserID = "local"
	}
	if config.Session == "" {
		config.Session = "local"
	}

	root := &cobra.Command{Use: "golem", Short: "Run a golem agent", SilenceUsage: true}
	root.AddCommand(newChatCommand(config), newCancelCommand(config), newVersionCommand(config.Output))
	return root
}

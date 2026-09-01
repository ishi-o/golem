package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino/compose"

	"github.com/ishi-o/golem/core/storage"
)

// LargeResponseMiddlewareConfig configures NewLargeResponseMiddleware.
type LargeResponseMiddlewareConfig struct {
	// GuideThreshold is the result length (in bytes) that triggers a divert.
	GuideThreshold int
	// Workspaces builds the per-user home the diverted file lands in.
	Workspaces *storage.WorkspaceFactory
	Log        Logger
}

// Logger is the small slice of slog the middleware needs; slog.Logger
// satisfies it. An interface rather than the concrete type so a test can
// capture the divert without a log buffer.
type Logger interface {
	Info(msg string, args ...any)
}

// NewLargeResponseMiddleware diverts an oversized tool result to a file in
// the user's workspace and hands the model a pointer instead. A large listing
// in the conversation crowds every later turn; a file and short pointer
// preserve the result without consuming the context window.
func NewLargeResponseMiddleware(config LargeResponseMiddlewareConfig) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				output, err := next(ctx, input)
				if err != nil {
					return nil, err
				}
				if output == nil {
					return nil, fmt.Errorf("tool %s returned nil output", input.Name)
				}
				if config.GuideThreshold <= 0 || len(output.Result) <= config.GuideThreshold {
					return output, nil
				}

				result, err := divertLargeResponse(ctx, config, input.Name, output.Result)
				if err != nil {
					return nil, err
				}
				return &compose.ToolOutput{Result: result}, nil
			}
		},
	}
}

func divertLargeResponse(ctx context.Context, config LargeResponseMiddlewareConfig, name, result string) (string, error) {
	userID, err := UserID.Get(ctx)
	if err != nil || userID == "" || config.Workspaces == nil {
		// No identity, or no workspace to divert to: the result passes through
		// rather than the tool call failing over presentation.
		return result, nil
	}
	home := config.Workspaces.ForOwner(userID)
	artifacts, err := home.Folder(storage.FolderArtifacts)
	if err != nil {
		return result, err
	}
	directory := filepath.Join(artifacts, "tool-results")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return result, err
	}
	file, err := os.CreateTemp(directory, safeToolFilename(name)+"-*.txt")
	if err != nil {
		return result, err
	}
	filePath := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(filePath)
		return result, err
	}
	if _, err := file.WriteString(result); err != nil {
		_ = file.Close()
		_ = os.Remove(filePath)
		return result, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(filePath)
		return result, err
	}
	if config.Log != nil {
		config.Log.Info("large tool result diverted to workspace", "tool", name, "bytes", len(result), "file", filePath)
	}
	return fmt.Sprintf(
		"This tool's result was %d bytes, too large for the conversation; it was saved to %s. Use ReadFile with a line window or GrepFiles on it to find what you need.",
		len(result), filePath), nil
}

func safeToolFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "tool"
	}
	value := b.String()
	if len(value) > 80 {
		return value[:80]
	}
	return value
}

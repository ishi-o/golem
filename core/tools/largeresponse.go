package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ishi-o/golem/core/storage"
)

// LargeResponseInterceptor diverts an oversized tool result to a file in the
// user's workspace and hands the model a pointer instead — spring-agent's
// LargeResponseInterceptor. A 50k-line listing pasted into the conversation
// buys one turn of usefulness and crowds every turn after it; the same
// listing in a file the model can Read and Grep is worth more and costs
// less.
type LargeResponseInterceptor struct {
	// GuideThreshold is the result length (in bytes) that triggers a divert.
	GuideThreshold int
	// Workspaces builds the per-user home the diverted file lands in.
	Workspaces *storage.WorkspaceFactory
	Log        Logger
}

// Logger is the small slice of slog the interceptor needs; slog.Logger
// satisfies it. An interface rather than the concrete type so a test can
// capture the divert without a log buffer.
type Logger interface {
	Info(msg string, args ...any)
}

// AfterCall implements Interceptor.
func (l *LargeResponseInterceptor) AfterCall(ctx context.Context, name, arguments, result string) (string, bool, error) {
	if l.GuideThreshold <= 0 || len(result) <= l.GuideThreshold {
		return result, false, nil
	}
	userID, err := UserID.Get(ctx)
	if err != nil || userID == "" {
		// No identity, no workspace to divert to: the result passes through
		// rather than the tool call failing over presentation.
		return result, false, nil
	}
	home := l.Workspaces.ForOwner(userID)
	artifacts, err := home.Folder(storage.FolderArtifacts)
	if err != nil {
		return result, false, err
	}
	path := filepath.Join(artifacts, "tool-results", fmt.Sprintf("%s-%d.txt", name, time.Now().UnixMilli()))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return result, false, err
	}
	if err := os.WriteFile(path, []byte(result), 0o644); err != nil {
		return result, false, err
	}
	if l.Log != nil {
		l.Log.Info("large tool result diverted to workspace", "tool", name, "bytes", len(result), "file", path)
	}
	return fmt.Sprintf(
		"This tool's result was %d bytes, too large for the conversation; it was saved to %s. Use ReadFile with a line window or GrepFiles on it to find what you need.",
		len(result), path), false, nil
}

// BeforeCall implements Interceptor.
func (l *LargeResponseInterceptor) BeforeCall(ctx context.Context, name, arguments string) (string, error) {
	return arguments, nil
}

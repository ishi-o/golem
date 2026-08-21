package tools

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
	directory := filepath.Join(artifacts, "tool-results")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return result, false, err
	}
	file, err := os.CreateTemp(directory, safeToolFilename(name)+"-*.txt")
	if err != nil {
		return result, false, err
	}
	filePath := file.Name()
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		_ = os.Remove(filePath)
		return result, false, err
	}
	if _, err := file.WriteString(result); err != nil {
		_ = file.Close()
		_ = os.Remove(filePath)
		return result, false, err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(filePath)
		return result, false, err
	}
	if l.Log != nil {
		l.Log.Info("large tool result diverted to workspace", "tool", name, "bytes", len(result), "file", filePath)
	}
	return fmt.Sprintf(
		"This tool's result was %d bytes, too large for the conversation; it was saved to %s. Use ReadFile with a line window or GrepFiles on it to find what you need.",
		len(result), filePath), false, nil
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

// BeforeCall implements Interceptor.
func (l *LargeResponseInterceptor) BeforeCall(ctx context.Context, name, arguments string) (string, error) {
	return arguments, nil
}

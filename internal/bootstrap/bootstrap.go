// Package bootstrap builds the default runtime for the repository's
// applications from environment variables. It is internal so applications in
// this repository can share setup without making provider and driver
// dependencies part of the public core module.
package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/tools"
	sqlxstore "github.com/ishi-o/golem/store/sqlx"
	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

const (
	apiKeyEnv  = "OPENAI_API_KEY"
	modelEnv   = "OPENAI_MODEL"
	baseURLEnv = "OPENAI_BASE_URL"
	sqliteEnv  = "GOLEM_SQLITE_PATH"
)

// Runtime owns the resources created for an application process.
type Runtime struct {
	Agent *agent.Agent
	db    *sqlx.DB
}

// New creates the default runtime. The model uses the OpenAI-compatible
// chat-completions protocol; OPENAI_BASE_URL may point at another compatible
// service. SQLite is the deliberately boring default store for local apps.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger) (*Runtime, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := cfg.Normalize(); err != nil {
		return nil, fmt.Errorf("bootstrap: normalize config: %w", err)
	}

	apiKey := strings.TrimSpace(os.Getenv(apiKeyEnv))
	if apiKey == "" {
		return nil, fmt.Errorf("bootstrap: %s is required", apiKeyEnv)
	}
	modelName := strings.TrimSpace(os.Getenv(modelEnv))
	if modelName == "" {
		return nil, fmt.Errorf("bootstrap: %s is required", modelEnv)
	}

	chatModel, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		Model:   modelName,
		BaseURL: strings.TrimSpace(os.Getenv(baseURLEnv)),
	})
	if err != nil {
		return nil, fmt.Errorf("bootstrap: create chat model: %w", err)
	}

	dbPath := strings.TrimSpace(os.Getenv(sqliteEnv))
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Storage.Location, "golem.db")
	}
	db, err := openSQLite(ctx, dbPath)
	if err != nil {
		return nil, err
	}

	backend, err := sqlxstore.New(db, sqlxstore.WithDialect(sqlxstore.DialectSQLite))
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bootstrap: create sqlite store: %w", err)
	}
	if err := backend.Migrate(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bootstrap: migrate sqlite store: %w", err)
	}

	workspaces := storage.NewWorkspaceFactory(cfg.Storage.Location)
	provider := tools.NewProvider(cfg, workspaces, backend, nil, tools.WithLogger(logger))
	runtime := &Runtime{db: db}
	runtime.Agent = agent.New(
		chatModel,
		backend,
		provider,
		cfg,
		agent.WithBackend(backend),
		agent.WithLogger(logger),
		agent.WithModelName(modelName),
	)
	return runtime, nil
}

// Close stops active runs and closes the application-owned database.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.Agent != nil {
		if err := r.Agent.Shutdown(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if r.db != nil {
		if err := r.db.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func openSQLite(ctx context.Context, path string) (*sqlx.DB, error) {
	if path != ":memory:" && !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, fmt.Errorf("bootstrap: create sqlite directory: %w", err)
		}
	}
	db, err := sqlx.Open("sqlite3", path)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: open sqlite: %w", err)
	}
	// A single connection keeps SQLite transactions predictable and also
	// supports :memory: when a caller uses it for a local smoke test.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("bootstrap: ping sqlite: %w", err)
	}
	return db, nil
}

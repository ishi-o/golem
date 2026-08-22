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
	"github.com/ishi-o/golem/core/schedule"
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
	// Runner fires the user's scheduled tasks; nil when no scheduler was
	// injected, in which case the schedule tools are not offered.
	Runner *schedule.Runner
	db     *sqlx.DB
}

// Option configures the runtime during construction.
type Option func(*options)

type options struct {
	// Scheduler arms the scheduled tasks; core ships none, so an
	// application wanting scheduled tasks wraps its scheduler library
	// (gocron, robfig/cron, ...) in the schedule.Scheduler interface and
	// passes it here.
	scheduler schedule.Scheduler
}

// WithScheduler enables the scheduled-task feature over one scheduler.
func WithScheduler(s schedule.Scheduler) Option {
	return func(o *options) { o.scheduler = s }
}

// New creates the default runtime. The model uses the OpenAI-compatible
// chat-completions protocol; OPENAI_BASE_URL may point at another compatible
// service. SQLite is the deliberately boring default store for local apps.
func New(ctx context.Context, cfg config.Config, logger *slog.Logger, opts ...Option) (*Runtime, error) {
	var o options
	for _, opt := range opts {
		if opt != nil {
			opt(&o)
		}
	}
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
	if o.scheduler != nil {
		// The runner fires the agent, so it is built after it and stopped
		// before it: Close must allow no new firing while old ones drain.
		runner, err := schedule.New(backend.ScheduledTasks(), runtime.Agent, schedule.Config{
			Prompt:    cfg.AI.ScheduledTaskPrompt,
			Scheduler: o.scheduler,
		}, logger)
		if err != nil {
			return nil, fmt.Errorf("bootstrap: create scheduler: %w", err)
		}
		if err := runner.Start(ctx); err != nil {
			return nil, fmt.Errorf("bootstrap: start scheduler: %w", err)
		}
		schedule.RegisterBuiltins(provider, schedule.NewTools(runner, backend.ScheduledTasks()))
		runtime.Runner = runner
	}
	return runtime, nil
}

// Close stops active runs and closes the application-owned database.
func (r *Runtime) Close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	var errs []error
	if r.Runner != nil {
		r.Runner.Stop()
	}
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

// Package sqlxstore provides the relational persistence implementation.
//
// The package deliberately accepts an already opened sqlx.DB instead of
// choosing a driver. sqlx is a thin mapper, not a database driver; making the
// caller choose the driver keeps this module useful for SQLite, PostgreSQL,
// and MySQL without pulling an unrelated driver into every binary.
package sqlxstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
	"github.com/jmoiron/sqlx"
)

// Dialect controls the small number of statements whose upsert syntax differs
// between relational databases. Queries otherwise use portable SQL.
type Dialect string

const (
	DialectSQLite      Dialect = "sqlite"
	DialectPostgres    Dialect = "postgres"
	DialectMySQL       Dialect = "mysql"
	DialectGeneric     Dialect = "generic"
	defaultTablePrefix         = "golem_"
)

// Option configures a Store.
type Option func(*Store)

// WithDialect selects the SQL dialect. SQLite is the default because it is
// the zero-configuration development backend; PostgreSQL and MySQL only
// change the upsert clauses.
func WithDialect(dialect Dialect) Option {
	return func(s *Store) {
		if dialect != "" {
			s.dialect = dialect
		}
	}
}

// WithTablePrefix changes the table prefix. It is useful when an application
// shares one database with another service. The prefix is validated before it
// reaches a query because table names cannot be bound as SQL parameters.
func WithTablePrefix(prefix string) Option {
	return func(s *Store) {
		if prefix != "" {
			s.tablePrefix = prefix
		}
	}
}

// Store implements all public persistence contracts and chat memory.
type Store struct {
	db          *sqlx.DB
	dialect     Dialect
	tablePrefix string
}

// SQLStore is kept as a descriptive alias for callers that want the backend
// name in a field type.
type SQLStore = Store

// New creates a store over db. It does not open or close db; ownership stays
// with the application that opened it.
func New(db *sqlx.DB, options ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("sqlx store: nil database")
	}
	s := &Store{db: db, dialect: DialectSQLite, tablePrefix: defaultTablePrefix}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	if !identifier.MatchString(s.tablePrefix) {
		return nil, fmt.Errorf("sqlx store: invalid table prefix %q", s.tablePrefix)
	}
	switch s.dialect {
	case DialectSQLite, DialectPostgres, DialectMySQL, DialectGeneric:
	default:
		return nil, fmt.Errorf("sqlx store: unsupported dialect %q", s.dialect)
	}
	return s, nil
}

// DB exposes the underlying handle for migrations owned by the application.
func (s *Store) DB() *sqlx.DB { return s.db }

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s *Store) table(name string) string { return s.tablePrefix + name }

// Migrate creates the tables used by this module. There is intentionally no
// migration framework: these records are simple append/update documents and
// the application can run this idempotent schema at startup.
func (s *Store) Migrate(ctx context.Context) error {
	keyType, statusType := s.columnTypes()
	statements := []string{
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
id %s PRIMARY KEY,
owner_id %s NOT NULL,
name %s NOT NULL,
transport TEXT NOT NULL,
url TEXT NOT NULL,
headers_json TEXT,
title TEXT,
version TEXT,
description TEXT,
website_url TEXT,
enabled INTEGER NOT NULL,
shared_with_json TEXT,
UNIQUE (owner_id, name)
)`, s.table("mcp_servers"), keyType, keyType, keyType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
id %s PRIMARY KEY,
user_id TEXT,
chat_id TEXT,
chat_type TEXT,
conversation_id %s,
root_message_id TEXT,
card_id TEXT,
questions_json TEXT,
status %s NOT NULL,
created_at TEXT,
expires_at TEXT
)`, s.table("pending_questions"), keyType, keyType, statusType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
id %s PRIMARY KEY,
owner_id TEXT,
visibility TEXT,
directory INTEGER NOT NULL,
entry_filename TEXT,
expires_at TEXT
)`, s.table("published_resources"), keyType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
id %s PRIMARY KEY,
user_id %s,
chat_id TEXT,
chat_type TEXT,
root_message_id TEXT,
task_text TEXT,
cron_expression TEXT,
scheduled_at TEXT,
expires_at TEXT,
background INTEGER NOT NULL,
status %s NOT NULL
)`, s.table("scheduled_tasks"), keyType, keyType, statusType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
id %s PRIMARY KEY,
owner_id %s NOT NULL,
name %s NOT NULL,
value TEXT,
UNIQUE (owner_id, name)
)`, s.table("shell_credentials"), keyType, keyType, keyType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
id %s PRIMARY KEY,
created_at TEXT
)`, s.table("processed_messages"), keyType),
		fmt.Sprintf(`CREATE TABLE IF NOT EXISTS %s (
conversation_id %s PRIMARY KEY,
messages_json TEXT NOT NULL
)`, s.table("chat_memory"), keyType),
	}
	for _, statement := range statements {
		if _, err := s.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("sqlx store: migrate: %w", err)
		}
	}
	indexes := []struct {
		name  string
		table string
		cols  string
	}{
		{name: s.table("pending_questions_conversation_status_idx"), table: s.table("pending_questions"), cols: "conversation_id, status"},
		{name: s.table("scheduled_tasks_status_idx"), table: s.table("scheduled_tasks"), cols: "status"},
		{name: s.table("scheduled_tasks_user_status_idx"), table: s.table("scheduled_tasks"), cols: "user_id, status"},
	}
	for _, index := range indexes {
		if err := s.createIndex(ctx, index.name, index.table, index.cols); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) columnTypes() (key, status string) {
	if s.dialect == DialectMySQL {
		return "VARCHAR(255)", "VARCHAR(64)"
	}
	return "TEXT", "TEXT"
}

func (s *Store) createIndex(ctx context.Context, name, table, columns string) error {
	if s.dialect != DialectMySQL {
		query := fmt.Sprintf("CREATE INDEX IF NOT EXISTS %s ON %s (%s)", name, table, columns)
		if _, err := s.db.ExecContext(ctx, query); err != nil {
			return fmt.Errorf("sqlx store: create index %s: %w", name, err)
		}
		return nil
	}
	var exists int
	query := `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name = ?`
	if err := s.db.GetContext(ctx, &exists, s.rebind(query), table, name); err != nil {
		return fmt.Errorf("sqlx store: inspect index %s: %w", name, err)
	}
	if exists != 0 {
		return nil
	}
	query = fmt.Sprintf("CREATE INDEX %s ON %s (%s)", name, table, columns)
	if _, err := s.db.ExecContext(ctx, query); err != nil {
		return fmt.Errorf("sqlx store: create index %s: %w", name, err)
	}
	return nil
}

type mcpRow struct {
	ID             string         `db:"id"`
	OwnerID        string         `db:"owner_id"`
	Name           string         `db:"name"`
	Transport      string         `db:"transport"`
	URL            string         `db:"url"`
	HeadersJSON    sql.NullString `db:"headers_json"`
	Title          sql.NullString `db:"title"`
	Version        sql.NullString `db:"version"`
	Description    sql.NullString `db:"description"`
	WebsiteURL     sql.NullString `db:"website_url"`
	Enabled        int            `db:"enabled"`
	SharedWithJSON sql.NullString `db:"shared_with_json"`
}

func (s *Store) Save(ctx context.Context, config dao.McpServerConfig) error {
	headers, err := marshal(config.Headers)
	if err != nil {
		return fmt.Errorf("mcp headers: %w", err)
	}
	sharedWith, err := marshal(config.SharedWith)
	if err != nil {
		return fmt.Errorf("mcp sharedWith: %w", err)
	}
	query := s.upsert(s.table("mcp_servers"),
		[]string{"id", "owner_id", "name", "transport", "url", "headers_json", "title", "version", "description", "website_url", "enabled", "shared_with_json"},
		[]string{"id", "owner_id", "name", "transport", "url", "headers_json", "title", "version", "description", "website_url", "enabled", "shared_with_json"})
	_, err = s.db.ExecContext(ctx, s.rebind(query), config.ID, config.OwnerID, config.Name, string(config.Transport), config.URL, headers, config.Title, config.Version, config.Description, config.WebsiteURL, boolInt(config.Enabled), sharedWith)
	return wrap("save MCP server", err)
}

func (s *Store) McpServerConfigs() dao.McpServerConfigRepo { return mcpRepo{s} }

func (s *Store) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.McpServerConfig, error) {
	var rows []mcpRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, transport, url, headers_json, title, version, description, website_url, enabled, shared_with_json FROM %s WHERE owner_id = ? ORDER BY id`, s.table("mcp_servers"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), ownerID); err != nil {
		return nil, wrap("find MCP servers by owner", err)
	}
	return mapMCP(rows)
}

func (s *Store) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.McpServerConfig, error) {
	var row mcpRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, transport, url, headers_json, title, version, description, website_url, enabled, shared_with_json FROM %s WHERE owner_id = ? AND name = ?`, s.table("mcp_servers"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), ownerID, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find MCP server by owner and name", err)
	}
	configs, err := mapMCP([]mcpRow{row})
	if err != nil || len(configs) == 0 {
		return nil, err
	}
	return &configs[0], nil
}

func (s *Store) ExistsByOwnerIDAndName(ctx context.Context, ownerID, name string) (bool, error) {
	var exists int
	query := fmt.Sprintf(`SELECT 1 FROM %s WHERE owner_id = ? AND name = ?`, s.table("mcp_servers"))
	err := s.db.GetContext(ctx, &exists, s.rebind(query), ownerID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, wrap("check MCP server", err)
}

func (s *Store) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE owner_id = ? AND name = ?`, s.table("mcp_servers"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), ownerID, name)
	return wrap("delete MCP server", err)
}

func (s *Store) FindBySharedWithIn(ctx context.Context, identifiers []string) ([]dao.McpServerConfig, error) {
	configs, err := s.allMCP(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		set[identifier] = struct{}{}
	}
	out := configs[:0]
	for _, config := range configs {
		for _, sharedWith := range config.SharedWith {
			if _, ok := set[sharedWith]; ok {
				out = append(out, config)
				break
			}
		}
	}
	return out, nil
}

func (s *Store) FindAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]dao.McpServerConfig, error) {
	owned, err := s.FindByOwnerID(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	shared, err := s.FindBySharedWithIn(ctx, identifiers)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(owned)+len(shared))
	out := make([]dao.McpServerConfig, 0, len(owned)+len(shared))
	for _, config := range append(owned, shared...) {
		if _, ok := seen[config.ID]; ok {
			continue
		}
		seen[config.ID] = struct{}{}
		out = append(out, config)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) allMCP(ctx context.Context) ([]dao.McpServerConfig, error) {
	var rows []mcpRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, transport, url, headers_json, title, version, description, website_url, enabled, shared_with_json FROM %s ORDER BY id`, s.table("mcp_servers"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query)); err != nil {
		return nil, wrap("list MCP servers", err)
	}
	return mapMCP(rows)
}

func mapMCP(rows []mcpRow) ([]dao.McpServerConfig, error) {
	out := make([]dao.McpServerConfig, 0, len(rows))
	for _, row := range rows {
		var headers map[string]string
		var sharedWith []string
		if err := unmarshal(row.HeadersJSON, &headers); err != nil {
			return nil, fmt.Errorf("decode MCP headers: %w", err)
		}
		if err := unmarshal(row.SharedWithJSON, &sharedWith); err != nil {
			return nil, fmt.Errorf("decode MCP sharedWith: %w", err)
		}
		out = append(out, dao.McpServerConfig{ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, Transport: dao.McpTransport(row.Transport), URL: row.URL, Headers: headers, Title: row.Title.String, Version: row.Version.String, Description: row.Description.String, WebsiteURL: row.WebsiteURL.String, Enabled: row.Enabled != 0, SharedWith: sharedWith})
	}
	return out, nil
}

type pendingRow struct {
	ID             string         `db:"id"`
	UserID         sql.NullString `db:"user_id"`
	ChatID         sql.NullString `db:"chat_id"`
	ChatType       sql.NullString `db:"chat_type"`
	ConversationID sql.NullString `db:"conversation_id"`
	RootMessageID  sql.NullString `db:"root_message_id"`
	CardID         sql.NullString `db:"card_id"`
	QuestionsJSON  sql.NullString `db:"questions_json"`
	Status         string         `db:"status"`
	CreatedAt      sql.NullString `db:"created_at"`
	ExpiresAt      sql.NullString `db:"expires_at"`
}

func (s *Store) PendingQuestions() dao.PendingQuestionRepo { return pendingRepo{s} }

func (s *Store) SavePendingQuestion(ctx context.Context, question dao.PendingQuestion) error {
	query := s.upsert(s.table("pending_questions"), []string{"id", "user_id", "chat_id", "chat_type", "conversation_id", "root_message_id", "card_id", "questions_json", "status", "created_at", "expires_at"}, []string{"id", "user_id", "chat_id", "chat_type", "conversation_id", "root_message_id", "card_id", "questions_json", "status", "created_at", "expires_at"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), question.ID, question.UserID, question.ChatID, question.ChatType, question.ConversationID, question.RootMessageID, question.CardID, question.QuestionsJSON, string(question.Status), timeValue(question.CreatedAt), timeValue(question.ExpiresAt))
	return wrap("save pending question", err)
}

func (s *Store) FindByID(ctx context.Context, id string) (*dao.PendingQuestion, error) {
	var row pendingRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, conversation_id, root_message_id, card_id, questions_json, status, created_at, expires_at FROM %s WHERE id = ?`, s.table("pending_questions"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find pending question", err)
	}
	question, err := pendingFromRow(row)
	if err != nil {
		return nil, err
	}
	return &question, nil
}

func (s *Store) FindByConversationIDAndStatus(ctx context.Context, conversationID string, status dao.PendingQuestionStatus) ([]dao.PendingQuestion, error) {
	var rows []pendingRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, conversation_id, root_message_id, card_id, questions_json, status, created_at, expires_at FROM %s WHERE conversation_id = ? AND status = ? ORDER BY id`, s.table("pending_questions"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), conversationID, string(status)); err != nil {
		return nil, wrap("find pending questions", err)
	}
	out := make([]dao.PendingQuestion, 0, len(rows))
	for _, row := range rows {
		question, err := pendingFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, question)
	}
	return out, nil
}

func (s *Store) UpdateStatus(ctx context.Context, id string, status dao.PendingQuestionStatus) error {
	query := fmt.Sprintf(`UPDATE %s SET status = ? WHERE id = ?`, s.table("pending_questions"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), string(status), id)
	return wrap("update pending question status", err)
}

func pendingFromRow(row pendingRow) (dao.PendingQuestion, error) {
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return dao.PendingQuestion{}, fmt.Errorf("decode pending question createdAt: %w", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return dao.PendingQuestion{}, fmt.Errorf("decode pending question expiresAt: %w", err)
	}
	return dao.PendingQuestion{ID: row.ID, UserID: row.UserID.String, ChatID: row.ChatID.String, ChatType: row.ChatType.String, ConversationID: row.ConversationID.String, RootMessageID: row.RootMessageID.String, CardID: row.CardID.String, QuestionsJSON: row.QuestionsJSON.String, Status: dao.PendingQuestionStatus(row.Status), CreatedAt: createdAt, ExpiresAt: expiresAt}, nil
}

type resourceRow struct {
	ID            string         `db:"id"`
	OwnerID       sql.NullString `db:"owner_id"`
	Visibility    sql.NullString `db:"visibility"`
	Directory     int            `db:"directory"`
	EntryFilename sql.NullString `db:"entry_filename"`
	ExpiresAt     sql.NullString `db:"expires_at"`
}

func (s *Store) PublishedResources() dao.PublishedResourceRepo { return resourceRepo{s} }

func (s *Store) SavePublishedResource(ctx context.Context, resource dao.PublishedResource) error {
	query := s.upsert(s.table("published_resources"), []string{"id", "owner_id", "visibility", "directory", "entry_filename", "expires_at"}, []string{"id", "owner_id", "visibility", "directory", "entry_filename", "expires_at"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), resource.ID, resource.OwnerID, string(resource.Visibility), boolInt(resource.Directory), resource.EntryFilename, timeValue(resource.ExpiresAt))
	return wrap("save published resource", err)
}

func (s *Store) FindPublishedResource(ctx context.Context, id string) (*dao.PublishedResource, error) {
	var row resourceRow
	query := fmt.Sprintf(`SELECT id, owner_id, visibility, directory, entry_filename, expires_at FROM %s WHERE id = ?`, s.table("published_resources"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find published resource", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("decode published resource expiry: %w", err)
	}
	return &dao.PublishedResource{ID: row.ID, OwnerID: row.OwnerID.String, Visibility: dao.Visibility(row.Visibility.String), Directory: row.Directory != 0, EntryFilename: row.EntryFilename.String, ExpiresAt: expiresAt}, nil
}

func (s *Store) DeletePublishedResource(ctx context.Context, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table("published_resources"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), id)
	return wrap("delete published resource", err)
}

type taskRow struct {
	ID             string         `db:"id"`
	UserID         sql.NullString `db:"user_id"`
	ChatID         sql.NullString `db:"chat_id"`
	ChatType       sql.NullString `db:"chat_type"`
	RootMessageID  sql.NullString `db:"root_message_id"`
	TaskText       sql.NullString `db:"task_text"`
	CronExpression sql.NullString `db:"cron_expression"`
	ScheduledAt    sql.NullString `db:"scheduled_at"`
	ExpiresAt      sql.NullString `db:"expires_at"`
	Background     int            `db:"background"`
	Status         string         `db:"status"`
}

func (s *Store) ScheduledTasks() dao.ScheduledTaskRepo { return taskRepo{s} }

func (s *Store) SaveScheduledTask(ctx context.Context, task dao.ScheduledTask) error {
	query := s.upsert(s.table("scheduled_tasks"), []string{"id", "user_id", "chat_id", "chat_type", "root_message_id", "task_text", "cron_expression", "scheduled_at", "expires_at", "background", "status"}, []string{"id", "user_id", "chat_id", "chat_type", "root_message_id", "task_text", "cron_expression", "scheduled_at", "expires_at", "background", "status"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), task.ID, task.UserID, task.ChatID, task.ChatType, task.RootMessageID, task.TaskText, task.CronExpression, timeValue(task.ScheduledAt), timeValue(task.ExpiresAt), boolInt(task.Background), string(task.Status))
	return wrap("save scheduled task", err)
}

func (s *Store) FindScheduledTaskByID(ctx context.Context, id string) (*dao.ScheduledTask, error) {
	var row taskRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, root_message_id, task_text, cron_expression, scheduled_at, expires_at, background, status FROM %s WHERE id = ?`, s.table("scheduled_tasks"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find scheduled task", err)
	}
	task, err := taskFromRow(row)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) FindByStatus(ctx context.Context, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return s.findTasks(ctx, `status = ?`, string(status))
}

func (s *Store) FindByUserIDAndStatus(ctx context.Context, userID string, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return s.findTasks(ctx, `user_id = ? AND status = ?`, userID, string(status))
}

func (s *Store) findTasks(ctx context.Context, where string, args ...any) ([]dao.ScheduledTask, error) {
	var rows []taskRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, root_message_id, task_text, cron_expression, scheduled_at, expires_at, background, status FROM %s WHERE %s ORDER BY id`, s.table("scheduled_tasks"), where)
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), args...); err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	out := make([]dao.ScheduledTask, 0, len(rows))
	for _, row := range rows {
		task, err := taskFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *Store) UpdateTaskStatus(ctx context.Context, id string, status dao.ScheduledTaskStatus) error {
	query := fmt.Sprintf(`UPDATE %s SET status = ? WHERE id = ?`, s.table("scheduled_tasks"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), string(status), id)
	return wrap("update scheduled task status", err)
}

func taskFromRow(row taskRow) (dao.ScheduledTask, error) {
	scheduledAt, err := parseTime(row.ScheduledAt)
	if err != nil {
		return dao.ScheduledTask{}, fmt.Errorf("decode scheduled task time: %w", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return dao.ScheduledTask{}, fmt.Errorf("decode scheduled task expiry: %w", err)
	}
	return dao.ScheduledTask{ID: row.ID, UserID: row.UserID.String, ChatID: row.ChatID.String, ChatType: row.ChatType.String, RootMessageID: row.RootMessageID.String, TaskText: row.TaskText.String, CronExpression: row.CronExpression.String, ScheduledAt: scheduledAt, ExpiresAt: expiresAt, Background: row.Background != 0, Status: dao.ScheduledTaskStatus(row.Status)}, nil
}

type credentialRow struct {
	ID      string         `db:"id"`
	OwnerID string         `db:"owner_id"`
	Name    string         `db:"name"`
	Value   sql.NullString `db:"value"`
}

func (s *Store) ShellCredentials() dao.ShellCredentialRepo { return credentialRepo{s} }

func (s *Store) SaveShellCredential(ctx context.Context, credential dao.ShellCredential) error {
	credential.ID = dao.ShellCredentialID(credential.OwnerID, credential.Name)
	query := s.upsert(s.table("shell_credentials"), []string{"id", "owner_id", "name", "value"}, []string{"id", "owner_id", "name", "value"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), credential.ID, credential.OwnerID, credential.Name, credential.Value)
	return wrap("save shell credential", err)
}

func (s *Store) FindByOwnerIDCredentials(ctx context.Context, ownerID string) ([]dao.ShellCredential, error) {
	var rows []credentialRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, value FROM %s WHERE owner_id = ? ORDER BY name`, s.table("shell_credentials"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), ownerID); err != nil {
		return nil, wrap("find shell credentials", err)
	}
	return mapCredentials(rows), nil
}

func (s *Store) FindByOwnerIDAndNameCredential(ctx context.Context, ownerID, name string) (*dao.ShellCredential, error) {
	var row credentialRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, value FROM %s WHERE owner_id = ? AND name = ?`, s.table("shell_credentials"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), ownerID, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find shell credential", err)
	}
	credentials := mapCredentials([]credentialRow{row})
	return &credentials[0], nil
}

func (s *Store) DeleteByOwnerIDAndNameCredential(ctx context.Context, ownerID, name string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE owner_id = ? AND name = ?`, s.table("shell_credentials"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), ownerID, name)
	return wrap("delete shell credential", err)
}

func mapCredentials(rows []credentialRow) []dao.ShellCredential {
	out := make([]dao.ShellCredential, 0, len(rows))
	for _, row := range rows {
		out = append(out, dao.ShellCredential{ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, Value: row.Value.String})
	}
	return out
}

func (s *Store) ProcessedMessages() dao.ProcessedMessageRepo { return processedRepo{s} }

func (s *Store) Claim(ctx context.Context, id string) (bool, error) {
	query := fmt.Sprintf(`INSERT INTO %s (id, created_at) VALUES (?, ?)`, s.table("processed_messages"))
	switch s.dialect {
	case DialectMySQL:
		query += ` ON DUPLICATE KEY UPDATE id = id`
	default:
		query += ` ON CONFLICT (id) DO NOTHING`
	}
	result, err := s.db.ExecContext(ctx, s.rebind(query), id, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, wrap("claim processed message", err)
	}
	count, err := result.RowsAffected()
	return count == 1, wrap("inspect processed message claim", err)
}

func (s *Store) Release(ctx context.Context, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table("processed_messages"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), id)
	return wrap("release processed message", err)
}

type memoryRow struct {
	ConversationID string `db:"conversation_id"`
	MessagesJSON   string `db:"messages_json"`
}

func (s *Store) Append(ctx context.Context, conversationID string, messages []*schema.Message) error {
	if conversationID == "" || len(messages) == 0 {
		return nil
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin chat memory append", err)
	}
	defer tx.Rollback()

	// Seed an absent conversation without replacing an existing one. The
	// subsequent read is then locked on databases that support row locks, so
	// concurrent appends cannot read the same old JSON and lose a turn.
	seed := fmt.Sprintf(`INSERT INTO %s (conversation_id, messages_json) VALUES (?, ?)`, s.table("chat_memory"))
	switch s.dialect {
	case DialectMySQL:
		seed += ` ON DUPLICATE KEY UPDATE conversation_id = conversation_id`
	default:
		seed += ` ON CONFLICT (conversation_id) DO NOTHING`
	}
	if _, err := tx.ExecContext(ctx, s.rebind(seed), conversationID, "[]"); err != nil {
		return wrap("seed chat memory append", err)
	}
	var current []*schema.Message
	var row memoryRow
	selectQuery := fmt.Sprintf(`SELECT conversation_id, messages_json FROM %s WHERE conversation_id = ?`, s.table("chat_memory"))
	if s.dialect == DialectPostgres || s.dialect == DialectMySQL {
		selectQuery += ` FOR UPDATE`
	}
	if err := tx.GetContext(ctx, &row, s.rebind(selectQuery), conversationID); err != nil {
		return wrap("load chat memory before append", err)
	}
	if err := json.Unmarshal([]byte(row.MessagesJSON), &current); err != nil {
		return fmt.Errorf("decode chat memory: %w", err)
	}
	current = append(current, messages...)
	payload, err := json.Marshal(current)
	if err != nil {
		return fmt.Errorf("encode chat memory: %w", err)
	}
	update := fmt.Sprintf(`UPDATE %s SET messages_json = ? WHERE conversation_id = ?`, s.table("chat_memory"))
	if _, err := tx.ExecContext(ctx, s.rebind(update), string(payload), conversationID); err != nil {
		return wrap("append chat memory", err)
	}
	return wrap("commit chat memory append", tx.Commit())
}

func (s *Store) Load(ctx context.Context, conversationID string, window int) ([]*schema.Message, error) {
	var row memoryRow
	query := fmt.Sprintf(`SELECT conversation_id, messages_json FROM %s WHERE conversation_id = ?`, s.table("chat_memory"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), conversationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []*schema.Message{}, nil
		}
		return nil, wrap("load chat memory", err)
	}
	var messages []*schema.Message
	if err := json.Unmarshal([]byte(row.MessagesJSON), &messages); err != nil {
		return nil, fmt.Errorf("decode chat memory: %w", err)
	}
	if window > 0 && len(messages) > window {
		messages = messages[len(messages)-window:]
	}
	return messages, nil
}

func (s *Store) Delete(ctx context.Context, conversationID string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE conversation_id = ?`, s.table("chat_memory"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), conversationID)
	return wrap("delete chat memory", err)
}

func (s *Store) ListConversations(ctx context.Context) ([]string, error) {
	var rows []struct {
		ID string `db:"conversation_id"`
	}
	query := fmt.Sprintf(`SELECT conversation_id FROM %s ORDER BY conversation_id`, s.table("chat_memory"))
	if err := s.db.SelectContext(ctx, &rows, query); err != nil {
		return nil, wrap("list chat conversations", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

func (s *Store) rebind(query string) string { return s.db.Rebind(query) }

func (s *Store) upsert(table string, columns, updates []string) string {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ", "), placeholders)
	switch s.dialect {
	case DialectMySQL:
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			assignments = append(assignments, column+" = VALUES("+column+")")
		}
		return query + " ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", ")
	case DialectGeneric:
		// Generic SQL has no portable upsert. The caller should use a
		// dialect for stores that need concurrent updates; this clause is
		// kept useful for engines that accept SQL-standard MERGE-like
		// aliases through ON CONFLICT.
		fallthrough
	default:
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			if column == columns[0] {
				continue
			}
			assignments = append(assignments, column+" = excluded."+column)
		}
		return query + " ON CONFLICT (" + columns[0] + ") DO UPDATE SET " + strings.Join(assignments, ", ")
	}
}

func marshal(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	return string(data), err
}

func unmarshal(value sql.NullString, target any) error {
	if !value.Valid || value.String == "" || value.String == "null" {
		return nil
	}
	return json.Unmarshal([]byte(value.String), target)
}

func timeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value.String)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlx store: %s: %w", operation, err)
}

var (
	_ dao.Backend           = (*Store)(nil)
	_ chatmemory.Repository = (*Store)(nil)
)

// The contracts intentionally reuse method names such as Save and FindByID.
// Small repository views keep those names readable without forcing one
// concrete Store to invent backend-specific suffixes in its public API.
type mcpRepo struct{ store *Store }

func (r mcpRepo) Save(ctx context.Context, value dao.McpServerConfig) error {
	return r.store.Save(ctx, value)
}
func (r mcpRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.McpServerConfig, error) {
	return r.store.FindByOwnerID(ctx, ownerID)
}
func (r mcpRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.McpServerConfig, error) {
	return r.store.FindByOwnerIDAndName(ctx, ownerID, name)
}
func (r mcpRepo) ExistsByOwnerIDAndName(ctx context.Context, ownerID, name string) (bool, error) {
	return r.store.ExistsByOwnerIDAndName(ctx, ownerID, name)
}
func (r mcpRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	return r.store.DeleteByOwnerIDAndName(ctx, ownerID, name)
}
func (r mcpRepo) FindBySharedWithIn(ctx context.Context, identifiers []string) ([]dao.McpServerConfig, error) {
	return r.store.FindBySharedWithIn(ctx, identifiers)
}
func (r mcpRepo) FindAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]dao.McpServerConfig, error) {
	return r.store.FindAccessibleTo(ctx, ownerID, identifiers)
}

type pendingRepo struct{ store *Store }

func (r pendingRepo) Save(ctx context.Context, value dao.PendingQuestion) error {
	return r.store.SavePendingQuestion(ctx, value)
}
func (r pendingRepo) FindByID(ctx context.Context, id string) (*dao.PendingQuestion, error) {
	return r.store.FindByID(ctx, id)
}
func (r pendingRepo) FindByConversationIDAndStatus(ctx context.Context, conversationID string, status dao.PendingQuestionStatus) ([]dao.PendingQuestion, error) {
	return r.store.FindByConversationIDAndStatus(ctx, conversationID, status)
}
func (r pendingRepo) UpdateStatus(ctx context.Context, id string, status dao.PendingQuestionStatus) error {
	return r.store.UpdateStatus(ctx, id, status)
}

type resourceRepo struct{ store *Store }

func (r resourceRepo) Save(ctx context.Context, value dao.PublishedResource) error {
	return r.store.SavePublishedResource(ctx, value)
}
func (r resourceRepo) FindByID(ctx context.Context, id string) (*dao.PublishedResource, error) {
	return r.store.FindPublishedResource(ctx, id)
}
func (r resourceRepo) DeleteByID(ctx context.Context, id string) error {
	return r.store.DeletePublishedResource(ctx, id)
}

type taskRepo struct{ store *Store }

func (r taskRepo) Save(ctx context.Context, value dao.ScheduledTask) error {
	return r.store.SaveScheduledTask(ctx, value)
}
func (r taskRepo) FindByID(ctx context.Context, id string) (*dao.ScheduledTask, error) {
	return r.store.FindScheduledTaskByID(ctx, id)
}
func (r taskRepo) FindByStatus(ctx context.Context, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return r.store.FindByStatus(ctx, status)
}
func (r taskRepo) FindByUserIDAndStatus(ctx context.Context, userID string, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return r.store.FindByUserIDAndStatus(ctx, userID, status)
}
func (r taskRepo) UpdateStatus(ctx context.Context, id string, status dao.ScheduledTaskStatus) error {
	return r.store.UpdateTaskStatus(ctx, id, status)
}

type credentialRepo struct{ store *Store }

func (r credentialRepo) Save(ctx context.Context, value dao.ShellCredential) error {
	return r.store.SaveShellCredential(ctx, value)
}
func (r credentialRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.ShellCredential, error) {
	return r.store.FindByOwnerIDCredentials(ctx, ownerID)
}
func (r credentialRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.ShellCredential, error) {
	return r.store.FindByOwnerIDAndNameCredential(ctx, ownerID, name)
}
func (r credentialRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	return r.store.DeleteByOwnerIDAndNameCredential(ctx, ownerID, name)
}

type processedRepo struct{ store *Store }

func (r processedRepo) Claim(ctx context.Context, id string) (bool, error) {
	return r.store.Claim(ctx, id)
}
func (r processedRepo) Release(ctx context.Context, id string) error {
	return r.store.Release(ctx, id)
}

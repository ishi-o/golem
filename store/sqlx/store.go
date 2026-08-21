// Package sqlxstore provides the relational persistence implementation.
//
// The package deliberately accepts an already opened sqlx.DB instead of
// choosing a driver. sqlx is a thin mapper, not a database driver; making the
// caller choose the driver keeps this module useful for SQLite, PostgreSQL,
// and MySQL without pulling an unrelated driver into every binary.
package sqlxstore

import (
	"context"
	"errors"
	"fmt"
	"regexp"

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

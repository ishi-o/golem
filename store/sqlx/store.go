// Package sqlx provides relational persistence through an existing
// sqlx.DB. The caller chooses and owns the database driver and connection.
//
// The implementation lives in the module-local internal package. This
// package keeps the public import path small and stable for downstream users.
package sqlx

import (
	implementation "github.com/ishi-o/golem/store/sqlx/internal"
	"github.com/jmoiron/sqlx"
)

// Dialect controls database-specific upsert syntax.
type Dialect = implementation.Dialect

const (
	DialectSQLite   = implementation.DialectSQLite
	DialectPostgres = implementation.DialectPostgres
	DialectMySQL    = implementation.DialectMySQL
	DialectGeneric  = implementation.DialectGeneric
)

// Option configures a Store.
type Option = implementation.Option

// Store implements the persistence and chat-memory contracts.
type Store = implementation.Store

// WithDialect selects the SQL dialect.
func WithDialect(dialect Dialect) Option {
	return implementation.WithDialect(dialect)
}

// WithTablePrefix changes the table-name prefix.
func WithTablePrefix(prefix string) Option {
	return implementation.WithTablePrefix(prefix)
}

// New creates a store over an existing sqlx.DB.
func New(db *sqlx.DB, options ...Option) (*Store, error) {
	return implementation.New(db, options...)
}

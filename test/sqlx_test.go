package agent_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/store"
	sqlxstore "github.com/ishi-o/golem/store/sqlx"
	"github.com/ishi-o/golem/test/storecontract"
	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sqlxFixture struct {
	db    *sqlx.DB
	store *sqlxstore.Store
	owner atomic.Uint64
}

func newSQLXFixture(t *testing.T) *sqlxFixture {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	database := sqlx.NewDb(db, "sqlite3")
	store, err := sqlxstore.New(database)
	require.NoError(t, err)
	require.NoError(t, store.Migrate(context.Background()))
	return &sqlxFixture{db: database, store: store}
}

func (f *sqlxFixture) Backend() store.Backend        { return f.store }
func (f *sqlxFixture) Memory() chatmemory.Repository { return f.store }
func (f *sqlxFixture) Owner() string {
	return fmt.Sprintf("sqlite-owner-%d", f.owner.Add(1))
}
func (f *sqlxFixture) Close() error { return f.db.Close() }

func TestSQLXPersistenceContract(t *testing.T) {
	fixture := newSQLXFixture(t)
	defer func() {
		assert.NoError(t, fixture.Close())
	}()
	storecontract.Run(t, fixture)
}

package agent_test

import (
	"context"
	"database/sql"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
	sqlxstore "github.com/ishi-o/golem/store/sqlx"
	"github.com/ishi-o/golem/test/daocontract"
	"github.com/jmoiron/sqlx"
	_ "github.com/mattn/go-sqlite3"
)

type sqlxFixture struct {
	db    *sqlx.DB
	store *sqlxstore.Store
	owner atomic.Uint64
}

func newSQLXFixture(t *testing.T) *sqlxFixture {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	database := sqlx.NewDb(db, "sqlite3")
	store, err := sqlxstore.New(database)
	if err != nil {
		database.Close()
		t.Fatal(err)
	}
	if err := store.Migrate(context.Background()); err != nil {
		database.Close()
		t.Fatal(err)
	}
	return &sqlxFixture{db: database, store: store}
}

func (f *sqlxFixture) Backend() dao.Backend          { return f.store }
func (f *sqlxFixture) Memory() chatmemory.Repository { return f.store }
func (f *sqlxFixture) Owner() string {
	return fmt.Sprintf("sqlite-owner-%d", f.owner.Add(1))
}
func (f *sqlxFixture) Close() error { return f.db.Close() }

func TestSQLXPersistenceContract(t *testing.T) {
	fixture := newSQLXFixture(t)
	defer func() {
		if err := fixture.Close(); err != nil {
			t.Errorf("close database: %v", err)
		}
	}()
	daocontract.Run(t, fixture)
}

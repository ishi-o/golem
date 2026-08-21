package agent_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
	mongostore "github.com/ishi-o/golem/store/mongodb"
	"github.com/ishi-o/golem/test/daocontract"
)

type mongoFixture struct {
	clientClose func() error
	store       *mongostore.Store
	owner       atomic.Uint64
}

func newMongoFixture(t *testing.T) *mongoFixture {
	t.Helper()
	uri := os.Getenv("GOLEM_TEST_MONGODB_URI")
	if uri == "" {
		t.Skip("GOLEM_TEST_MONGODB_URI is not set")
	}
	database := os.Getenv("GOLEM_TEST_MONGODB_DATABASE")
	if database == "" {
		database = "golem_test"
	}
	database = fmt.Sprintf("%s_%d", database, time.Now().UnixNano())
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	store, client, err := mongostore.Connect(ctx, uri, database)
	if err != nil {
		t.Skipf("MongoDB is unavailable: %v", err)
	}
	if err := store.Migrate(ctx); err != nil {
		_ = client.Disconnect(context.Background())
		t.Fatal(err)
	}
	return &mongoFixture{clientClose: func() error { return client.Disconnect(context.Background()) }, store: store}
}

func (f *mongoFixture) Backend() dao.Backend          { return f.store }
func (f *mongoFixture) Memory() chatmemory.Repository { return f.store }
func (f *mongoFixture) Owner() string {
	return fmt.Sprintf("mongo-owner-%d", f.owner.Add(1))
}
func (f *mongoFixture) Close() error { return f.clientClose() }

func TestMongoPersistenceContract(t *testing.T) {
	fixture := newMongoFixture(t)
	defer func() {
		if err := fixture.Close(); err != nil {
			t.Errorf("close MongoDB: %v", err)
		}
	}()
	daocontract.Run(t, fixture)
}

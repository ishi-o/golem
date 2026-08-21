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
	redisstore "github.com/ishi-o/golem/store/redis"
	"github.com/ishi-o/golem/test/daocontract"
	"github.com/redis/go-redis/v9"
)

type redisFixture struct {
	client *redis.Client
	store  *redisstore.Store
	owner  atomic.Uint64
}

func newRedisFixture(t *testing.T) *redisFixture {
	t.Helper()
	address := os.Getenv("GOLEM_TEST_REDIS_ADDR")
	if address == "" {
		t.Skip("GOLEM_TEST_REDIS_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: address, Username: os.Getenv("GOLEM_TEST_REDIS_USERNAME"), Password: os.Getenv("GOLEM_TEST_REDIS_PASSWORD")})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		t.Skipf("Redis is unavailable: %v", err)
	}
	prefix := fmt.Sprintf("golem:test:%d:", time.Now().UnixNano())
	store, err := redisstore.New(client, redisstore.WithKeyPrefix(prefix))
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	if err := store.Migrate(ctx); err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	return &redisFixture{client: client, store: store}
}

func (f *redisFixture) Backend() dao.Backend          { return f.store }
func (f *redisFixture) Memory() chatmemory.Repository { return f.store }
func (f *redisFixture) Owner() string {
	return fmt.Sprintf("redis-owner-%d", f.owner.Add(1))
}
func (f *redisFixture) Close() error { return f.client.Close() }

func TestRedisPersistenceContract(t *testing.T) {
	fixture := newRedisFixture(t)
	defer func() {
		if err := fixture.Close(); err != nil {
			t.Errorf("close Redis: %v", err)
		}
	}()
	daocontract.Run(t, fixture)
}

package agent_test

import (
	"context"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/store"
	redisstore "github.com/ishi-o/golem/store/redis"
	"github.com/ishi-o/golem/test/storecontract"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
	require.NoError(t, err)
	require.NoError(t, store.Migrate(ctx))
	return &redisFixture{client: client, store: store}
}

func (f *redisFixture) Backend() store.Backend        { return f.store }
func (f *redisFixture) Memory() chatmemory.Repository { return f.store }
func (f *redisFixture) Owner() string {
	return fmt.Sprintf("redis-owner-%d", f.owner.Add(1))
}
func (f *redisFixture) Close() error { return f.client.Close() }

func TestRedisPersistenceContract(t *testing.T) {
	fixture := newRedisFixture(t)
	defer func() {
		assert.NoError(t, fixture.Close())
	}()
	storecontract.Run(t, fixture)
}

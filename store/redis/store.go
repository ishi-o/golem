// Package redis provides Redis store.
//
// The implementation lives in the module-local internal package. Callers
// import this package for the public adapter API and own the Redis client.
package redis

import (
	"github.com/redis/go-redis/v9"

	implementation "github.com/ishi-o/golem/store/redis/internal"
)

// Option configures a Store.
type Option = implementation.Option

// Store implements the persistence and chat-memory contracts.
type Store = implementation.Store

// WithKeyPrefix changes the prefix used for Redis keys.
func WithKeyPrefix(prefix string) Option {
	return implementation.WithKeyPrefix(prefix)
}

// New creates a store over an existing Redis client.
func New(client redis.UniversalClient, options ...Option) (*Store, error) {
	return implementation.New(client, options...)
}

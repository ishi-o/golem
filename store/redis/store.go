// Package redisstore provides the Redis persistence implementation.
//
// Redis has no query planner. Every filter in the core contract therefore has
// an explicit set index maintained beside the JSON record. A query never
// scans every record, and a status update removes the old index entry before
// adding the new one.
package redisstore

import (
	"context"
	"encoding/base64"
	"errors"

	"github.com/redis/go-redis/v9"
)

// Option configures a Store.
type Option func(*Store)

// WithKeyPrefix changes the prefix used for every key. The default is
// `golem:` so this backend can share a Redis database without claiming a
// generic key namespace.
func WithKeyPrefix(prefix string) Option {
	return func(s *Store) {
		if prefix != "" {
			s.keyPrefix = prefix
		}
	}
}

// Store implements all public persistence contracts and chat memory.
type Store struct {
	client    redis.UniversalClient
	keyPrefix string
}

// RedisStore is a descriptive alias for callers that prefer the backend name.
type RedisStore = Store

// New creates a store over a caller-owned Redis client. The store does not
// close it; the application owns the connection lifecycle.
func New(client redis.UniversalClient, options ...Option) (*Store, error) {
	if client == nil {
		return nil, errors.New("redis store: nil client")
	}
	s := &Store{client: client, keyPrefix: "golem:"}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	return s, nil
}

// Client exposes the caller-owned client for health checks and shutdown.
func (s *Store) Client() redis.UniversalClient { return s.client }

// Migrate is intentionally a no-op. Redis creates keys on first write; the
// index maintenance in this package is the schema.
func (s *Store) Migrate(context.Context) error { return nil }

func (s *Store) key(parts ...string) string {
	result := s.keyPrefix
	for _, part := range parts {
		result += part
	}
	return result
}

func encoded(value string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(value))
}

func (s *Store) record(kind, id string) string   { return s.key(kind, "record:", encoded(id)) }
func (s *Store) all(kind string) string          { return s.key(kind, "all") }
func (s *Store) index(kind, value string) string { return s.key(kind, "index:", encoded(value)) }

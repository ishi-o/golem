// Package mongodb provides MongoDB store.
//
// The implementation lives in the module-local internal package. Callers
// import this package for the public adapter API and own the MongoDB client.
package mongodb

import (
	"context"

	"go.mongodb.org/mongo-driver/mongo"

	implementation "github.com/ishi-o/golem/store/mongodb/internal"
)

// Option configures a Store.
type Option = implementation.Option

// Store implements the persistence and chat-memory contracts.
type Store = implementation.Store

// WithCollectionPrefix changes the collection-name prefix.
func WithCollectionPrefix(prefix string) Option {
	return implementation.WithCollectionPrefix(prefix)
}

// New creates a store over an existing MongoDB database.
func New(db *mongo.Database, options ...Option) (*Store, error) {
	return implementation.New(db, options...)
}

// Connect opens and pings a MongoDB client, returning the store and client.
func Connect(ctx context.Context, uri, database string, options ...Option) (*Store, *mongo.Client, error) {
	return implementation.Connect(ctx, uri, database, options...)
}

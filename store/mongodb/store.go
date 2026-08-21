// Package mongostore provides the MongoDB persistence implementation.
//
// Documents are owned by this module rather than carrying MongoDB tags in
// core. That keeps the public core module free of driver types and lets a
// consumer import only the adapter it selected.
package mongostore

import (
	"context"
	"errors"
	"fmt"
	"regexp"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

// Option configures a Store.
type Option func(*Store)

// WithCollectionPrefix changes the collection prefix. The default keeps the
// application's records visibly separate from other documents in the same
// database.
func WithCollectionPrefix(prefix string) Option {
	return func(s *Store) {
		if prefix != "" {
			s.collectionPrefix = prefix
		}
	}
}

// Store implements all public persistence contracts and chat memory.
type Store struct {
	db               *mongo.Database
	collectionPrefix string
}

// MongoStore is a descriptive alias for callers that prefer the backend name
// in their wiring code.
type MongoStore = Store

// New creates a store over an existing database. The caller owns the client
// and is responsible for closing it.
func New(db *mongo.Database, options ...Option) (*Store, error) {
	if db == nil {
		return nil, errors.New("mongodb store: nil database")
	}
	s := &Store{db: db, collectionPrefix: "golem_"}
	for _, option := range options {
		if option != nil {
			option(s)
		}
	}
	if !identifier.MatchString(s.collectionPrefix) {
		return nil, fmt.Errorf("mongodb store: invalid collection prefix %q", s.collectionPrefix)
	}
	return s, nil
}

// Connect opens and pings a MongoDB client, returning both the store and the
// client so the application can close the client during shutdown.
func Connect(ctx context.Context, uri, database string, options ...Option) (*Store, *mongo.Client, error) {
	client, err := mongo.Connect(ctx, optionsForURI(uri))
	if err != nil {
		return nil, nil, fmt.Errorf("mongodb store: connect: %w", err)
	}
	if err := client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return nil, nil, fmt.Errorf("mongodb store: ping: %w", err)
	}
	store, err := New(client.Database(database), options...)
	if err != nil {
		_ = client.Disconnect(context.Background())
		return nil, nil, err
	}
	return store, client, nil
}

func optionsForURI(uri string) *options.ClientOptions {
	return options.Client().ApplyURI(uri)
}

var identifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

func (s *Store) collection(name string) *mongo.Collection {
	return s.db.Collection(s.collectionPrefix + name)
}

// Migrate creates the indexes used by the repository queries. MongoDB creates
// collections lazily, so there is no schema DDL beyond these indexes.
func (s *Store) Migrate(ctx context.Context) error {
	indexes := []struct {
		name   string
		keys   bson.D
		unique bool
	}{
		{"owner_name_unique", bson.D{{Key: "owner_id", Value: 1}, {Key: "name", Value: 1}}, true},
		{"shared_with", bson.D{{Key: "shared_with", Value: 1}}, false},
	}
	if err := createIndexes(ctx, s.collection("mcp_servers"), indexes); err != nil {
		return err
	}
	if err := createIndexes(ctx, s.collection("pending_questions"), []struct {
		name   string
		keys   bson.D
		unique bool
	}{{"conversation_status", bson.D{{Key: "conversation_id", Value: 1}, {Key: "status", Value: 1}}, false}}); err != nil {
		return err
	}
	if err := createIndexes(ctx, s.collection("scheduled_tasks"), []struct {
		name   string
		keys   bson.D
		unique bool
	}{{"status", bson.D{{Key: "status", Value: 1}}, false}, {"user_status", bson.D{{Key: "user_id", Value: 1}, {Key: "status", Value: 1}}, false}}); err != nil {
		return err
	}
	if err := createIndexes(ctx, s.collection("shell_credentials"), []struct {
		name   string
		keys   bson.D
		unique bool
	}{{"owner_name_unique", bson.D{{Key: "owner_id", Value: 1}, {Key: "name", Value: 1}}, true}}); err != nil {
		return err
	}
	return nil
}

func createIndexes(ctx context.Context, collection *mongo.Collection, indexes []struct {
	name   string
	keys   bson.D
	unique bool
}) error {
	models := make([]mongo.IndexModel, 0, len(indexes))
	for _, index := range indexes {
		models = append(models, mongo.IndexModel{Keys: index.keys, Options: options.Index().SetName(index.name).SetUnique(index.unique)})
	}
	if _, err := collection.Indexes().CreateMany(ctx, models); err != nil {
		return fmt.Errorf("mongodb store: create indexes on %s: %w", collection.Name(), err)
	}
	return nil
}

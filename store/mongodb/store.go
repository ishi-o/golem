// Package mongostore provides the MongoDB persistence implementation.
//
// Documents are owned by this module rather than carrying MongoDB tags in
// core. That keeps the public core module free of driver types and lets a
// consumer import only the adapter it selected.
package mongostore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
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

type mcpDocument struct {
	ID          string            `bson:"_id"`
	OwnerID     string            `bson:"owner_id"`
	Name        string            `bson:"name"`
	Transport   dao.McpTransport  `bson:"transport"`
	URL         string            `bson:"url"`
	Headers     map[string]string `bson:"headers,omitempty"`
	Title       string            `bson:"title,omitempty"`
	Version     string            `bson:"version,omitempty"`
	Description string            `bson:"description,omitempty"`
	WebsiteURL  string            `bson:"website_url,omitempty"`
	Enabled     bool              `bson:"enabled"`
	SharedWith  []string          `bson:"shared_with,omitempty"`
}

func (s *Store) McpServerConfigs() dao.McpServerConfigRepo { return mcpRepo{s} }

func (s *Store) saveMCP(ctx context.Context, value dao.McpServerConfig) error {
	document := mcpDocument{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Transport: value.Transport, URL: value.URL, Headers: value.Headers, Title: value.Title, Version: value.Version, Description: value.Description, WebsiteURL: value.WebsiteURL, Enabled: value.Enabled, SharedWith: value.SharedWith}
	_, err := s.collection("mcp_servers").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save MCP server", err)
}

func (s *Store) findMCPBy(ctx context.Context, filter bson.M) ([]dao.McpServerConfig, error) {
	cursor, err := s.collection("mcp_servers").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("find MCP servers", err)
	}
	defer cursor.Close(ctx)
	var documents []mcpDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode MCP servers", err)
	}
	out := make([]dao.McpServerConfig, 0, len(documents))
	for _, document := range documents {
		out = append(out, dao.McpServerConfig{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Transport: document.Transport, URL: document.URL, Headers: document.Headers, Title: document.Title, Version: document.Version, Description: document.Description, WebsiteURL: document.WebsiteURL, Enabled: document.Enabled, SharedWith: document.SharedWith})
	}
	return out, nil
}

func (s *Store) findOneMCP(ctx context.Context, filter bson.M) (*dao.McpServerConfig, error) {
	var document mcpDocument
	err := s.collection("mcp_servers").FindOne(ctx, filter).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap("find MCP server", err)
	}
	value := dao.McpServerConfig{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Transport: document.Transport, URL: document.URL, Headers: document.Headers, Title: document.Title, Version: document.Version, Description: document.Description, WebsiteURL: document.WebsiteURL, Enabled: document.Enabled, SharedWith: document.SharedWith}
	return &value, nil
}

type pendingDocument struct {
	ID             string    `bson:"_id"`
	UserID         string    `bson:"user_id,omitempty"`
	ChatID         string    `bson:"chat_id,omitempty"`
	ChatType       string    `bson:"chat_type,omitempty"`
	ConversationID string    `bson:"conversation_id,omitempty"`
	RootMessageID  string    `bson:"root_message_id,omitempty"`
	CardID         string    `bson:"card_id,omitempty"`
	QuestionsJSON  string    `bson:"questions_json,omitempty"`
	Status         string    `bson:"status"`
	CreatedAt      time.Time `bson:"created_at,omitempty"`
	ExpiresAt      time.Time `bson:"expires_at,omitempty"`
}

func (s *Store) PendingQuestions() dao.PendingQuestionRepo { return pendingRepo{s} }

func (s *Store) savePending(ctx context.Context, value dao.PendingQuestion) error {
	document := pendingDocument{ID: value.ID, UserID: value.UserID, ChatID: value.ChatID, ChatType: value.ChatType, ConversationID: value.ConversationID, RootMessageID: value.RootMessageID, CardID: value.CardID, QuestionsJSON: value.QuestionsJSON, Status: string(value.Status), CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt}
	_, err := s.collection("pending_questions").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save pending question", err)
}

func (s *Store) findPending(ctx context.Context, filter bson.M) ([]dao.PendingQuestion, error) {
	cursor, err := s.collection("pending_questions").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("find pending questions", err)
	}
	defer cursor.Close(ctx)
	var documents []pendingDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode pending questions", err)
	}
	out := make([]dao.PendingQuestion, 0, len(documents))
	for _, document := range documents {
		out = append(out, pendingValue(document))
	}
	return out, nil
}

func pendingValue(document pendingDocument) dao.PendingQuestion {
	return dao.PendingQuestion{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, ConversationID: document.ConversationID, RootMessageID: document.RootMessageID, CardID: document.CardID, QuestionsJSON: document.QuestionsJSON, Status: dao.PendingQuestionStatus(document.Status), CreatedAt: document.CreatedAt, ExpiresAt: document.ExpiresAt}
}

type resourceDocument struct {
	ID            string    `bson:"_id"`
	OwnerID       string    `bson:"owner_id,omitempty"`
	Visibility    string    `bson:"visibility,omitempty"`
	Directory     bool      `bson:"directory"`
	EntryFilename string    `bson:"entry_filename,omitempty"`
	ExpiresAt     time.Time `bson:"expires_at,omitempty"`
}

func (s *Store) PublishedResources() dao.PublishedResourceRepo { return resourceRepo{s} }

func (s *Store) saveResource(ctx context.Context, value dao.PublishedResource) error {
	document := resourceDocument{ID: value.ID, OwnerID: value.OwnerID, Visibility: string(value.Visibility), Directory: value.Directory, EntryFilename: value.EntryFilename, ExpiresAt: value.ExpiresAt}
	_, err := s.collection("published_resources").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save published resource", err)
}

func (s *Store) findResource(ctx context.Context, id string) (*dao.PublishedResource, error) {
	var document resourceDocument
	err := s.collection("published_resources").FindOne(ctx, bson.M{"_id": id}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap("find published resource", err)
	}
	return &dao.PublishedResource{ID: document.ID, OwnerID: document.OwnerID, Visibility: dao.Visibility(document.Visibility), Directory: document.Directory, EntryFilename: document.EntryFilename, ExpiresAt: document.ExpiresAt}, nil
}

type taskDocument struct {
	ID             string    `bson:"_id"`
	UserID         string    `bson:"user_id,omitempty"`
	ChatID         string    `bson:"chat_id,omitempty"`
	ChatType       string    `bson:"chat_type,omitempty"`
	RootMessageID  string    `bson:"root_message_id,omitempty"`
	TaskText       string    `bson:"task_text,omitempty"`
	CronExpression string    `bson:"cron_expression,omitempty"`
	ScheduledAt    time.Time `bson:"scheduled_at,omitempty"`
	ExpiresAt      time.Time `bson:"expires_at,omitempty"`
	Background     bool      `bson:"background"`
	Status         string    `bson:"status"`
}

func (s *Store) ScheduledTasks() dao.ScheduledTaskRepo { return taskRepo{s} }

func (s *Store) saveTask(ctx context.Context, value dao.ScheduledTask) error {
	document := taskDocument{ID: value.ID, UserID: value.UserID, ChatID: value.ChatID, ChatType: value.ChatType, RootMessageID: value.RootMessageID, TaskText: value.TaskText, CronExpression: value.CronExpression, ScheduledAt: value.ScheduledAt, ExpiresAt: value.ExpiresAt, Background: value.Background, Status: string(value.Status)}
	_, err := s.collection("scheduled_tasks").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save scheduled task", err)
}

func (s *Store) findOneTask(ctx context.Context, id string) (*dao.ScheduledTask, error) {
	var document taskDocument
	err := s.collection("scheduled_tasks").FindOne(ctx, bson.M{"_id": id}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap("find scheduled task", err)
	}
	return taskValue(document), nil
}

func (s *Store) findTasks(ctx context.Context, filter bson.M) ([]dao.ScheduledTask, error) {
	cursor, err := s.collection("scheduled_tasks").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	defer cursor.Close(ctx)
	var documents []taskDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode scheduled tasks", err)
	}
	out := make([]dao.ScheduledTask, 0, len(documents))
	for _, document := range documents {
		out = append(out, *taskValue(document))
	}
	return out, nil
}

func taskValue(document taskDocument) *dao.ScheduledTask {
	return &dao.ScheduledTask{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, RootMessageID: document.RootMessageID, TaskText: document.TaskText, CronExpression: document.CronExpression, ScheduledAt: document.ScheduledAt, ExpiresAt: document.ExpiresAt, Background: document.Background, Status: dao.ScheduledTaskStatus(document.Status)}
}

type credentialDocument struct {
	ID      string `bson:"_id"`
	OwnerID string `bson:"owner_id"`
	Name    string `bson:"name"`
	Value   string `bson:"value,omitempty"`
}

func (s *Store) ShellCredentials() dao.ShellCredentialRepo { return credentialRepo{s} }

func (s *Store) saveCredential(ctx context.Context, value dao.ShellCredential) error {
	value.ID = dao.ShellCredentialID(value.OwnerID, value.Name)
	document := credentialDocument{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Value: value.Value}
	_, err := s.collection("shell_credentials").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save shell credential", err)
}

func (s *Store) findCredentials(ctx context.Context, filter bson.M) ([]dao.ShellCredential, error) {
	cursor, err := s.collection("shell_credentials").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, wrap("find shell credentials", err)
	}
	defer cursor.Close(ctx)
	var documents []credentialDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode shell credentials", err)
	}
	out := make([]dao.ShellCredential, 0, len(documents))
	for _, document := range documents {
		out = append(out, dao.ShellCredential{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Value: document.Value})
	}
	return out, nil
}

func (s *Store) ProcessedMessages() dao.ProcessedMessageRepo { return processedRepo{s} }

func (s *Store) claim(ctx context.Context, id string) (bool, error) {
	_, err := s.collection("processed_messages").InsertOne(ctx, bson.M{"_id": id, "created_at": time.Now().UTC()})
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	return err == nil, wrap("claim processed message", err)
}

func (s *Store) release(ctx context.Context, id string) error {
	_, err := s.collection("processed_messages").DeleteOne(ctx, bson.M{"_id": id})
	return wrap("release processed message", err)
}

type memoryDocument struct {
	ID       string   `bson:"_id"`
	Messages []string `bson:"messages"`
}

func (s *Store) Append(ctx context.Context, conversationID string, messages []*schema.Message) error {
	if conversationID == "" || len(messages) == 0 {
		return nil
	}
	encoded := make([]string, 0, len(messages))
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("encode chat message: %w", err)
		}
		encoded = append(encoded, string(data))
	}
	_, err := s.collection("chat_memory").UpdateOne(ctx, bson.M{"_id": conversationID}, bson.M{"$setOnInsert": bson.M{"_id": conversationID}, "$push": bson.M{"messages": bson.M{"$each": encoded}}}, options.Update().SetUpsert(true))
	return wrap("append chat memory", err)
}

func (s *Store) Load(ctx context.Context, conversationID string, window int) ([]*schema.Message, error) {
	var document memoryDocument
	err := s.collection("chat_memory").FindOne(ctx, bson.M{"_id": conversationID}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return []*schema.Message{}, nil
	}
	if err != nil {
		return nil, wrap("load chat memory", err)
	}
	start := 0
	if window > 0 && len(document.Messages) > window {
		start = len(document.Messages) - window
	}
	out := make([]*schema.Message, 0, len(document.Messages)-start)
	for _, encoded := range document.Messages[start:] {
		var message schema.Message
		if err := json.Unmarshal([]byte(encoded), &message); err != nil {
			return nil, fmt.Errorf("decode chat message: %w", err)
		}
		out = append(out, &message)
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, conversationID string) error {
	_, err := s.collection("chat_memory").DeleteOne(ctx, bson.M{"_id": conversationID})
	return wrap("delete chat memory", err)
}

func (s *Store) ListConversations(ctx context.Context) ([]string, error) {
	cursor, err := s.collection("chat_memory").Find(ctx, bson.D{}, options.Find().SetProjection(bson.M{"_id": 1}).SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("list chat conversations", err)
	}
	defer cursor.Close(ctx)
	var documents []memoryDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode chat conversations", err)
	}
	ids := make([]string, 0, len(documents))
	for _, document := range documents {
		ids = append(ids, document.ID)
	}
	sort.Strings(ids)
	return ids, nil
}

type mcpRepo struct{ store *Store }

func (r mcpRepo) Save(ctx context.Context, value dao.McpServerConfig) error {
	return r.store.saveMCP(ctx, value)
}
func (r mcpRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.McpServerConfig, error) {
	return r.store.findMCPBy(ctx, bson.M{"owner_id": ownerID})
}
func (r mcpRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.McpServerConfig, error) {
	return r.store.findOneMCP(ctx, bson.M{"owner_id": ownerID, "name": name})
}
func (r mcpRepo) ExistsByOwnerIDAndName(ctx context.Context, ownerID, name string) (bool, error) {
	count, err := r.store.collection("mcp_servers").CountDocuments(ctx, bson.M{"owner_id": ownerID, "name": name})
	return count > 0, wrap("check MCP server", err)
}
func (r mcpRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	_, err := r.store.collection("mcp_servers").DeleteOne(ctx, bson.M{"owner_id": ownerID, "name": name})
	return wrap("delete MCP server", err)
}
func (r mcpRepo) FindBySharedWithIn(ctx context.Context, identifiers []string) ([]dao.McpServerConfig, error) {
	if len(identifiers) == 0 {
		return []dao.McpServerConfig{}, nil
	}
	return r.store.findMCPBy(ctx, bson.M{"shared_with": bson.M{"$in": identifiers}})
}
func (r mcpRepo) FindAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]dao.McpServerConfig, error) {
	filters := []bson.M{{"owner_id": ownerID}}
	if len(identifiers) > 0 {
		filters = append(filters, bson.M{"shared_with": bson.M{"$in": identifiers}})
	}
	values, err := r.store.findMCPBy(ctx, bson.M{"$or": filters})
	if err != nil {
		return nil, err
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ID < values[j].ID })
	return values, nil
}

type pendingRepo struct{ store *Store }

func (r pendingRepo) Save(ctx context.Context, value dao.PendingQuestion) error {
	return r.store.savePending(ctx, value)
}
func (r pendingRepo) FindByID(ctx context.Context, id string) (*dao.PendingQuestion, error) {
	var document pendingDocument
	err := r.store.collection("pending_questions").FindOne(ctx, bson.M{"_id": id}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap("find pending question", err)
	}
	value := pendingValue(document)
	return &value, nil
}
func (r pendingRepo) FindByConversationIDAndStatus(ctx context.Context, conversationID string, status dao.PendingQuestionStatus) ([]dao.PendingQuestion, error) {
	return r.store.findPending(ctx, bson.M{"conversation_id": conversationID, "status": string(status)})
}
func (r pendingRepo) UpdateStatus(ctx context.Context, id string, status dao.PendingQuestionStatus) error {
	_, err := r.store.collection("pending_questions").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"status": string(status)}})
	return wrap("update pending question status", err)
}

type resourceRepo struct{ store *Store }

func (r resourceRepo) Save(ctx context.Context, value dao.PublishedResource) error {
	return r.store.saveResource(ctx, value)
}
func (r resourceRepo) FindByID(ctx context.Context, id string) (*dao.PublishedResource, error) {
	return r.store.findResource(ctx, id)
}
func (r resourceRepo) DeleteByID(ctx context.Context, id string) error {
	_, err := r.store.collection("published_resources").DeleteOne(ctx, bson.M{"_id": id})
	return wrap("delete published resource", err)
}

type taskRepo struct{ store *Store }

func (r taskRepo) Save(ctx context.Context, value dao.ScheduledTask) error {
	return r.store.saveTask(ctx, value)
}
func (r taskRepo) FindByID(ctx context.Context, id string) (*dao.ScheduledTask, error) {
	return r.store.findOneTask(ctx, id)
}
func (r taskRepo) FindByStatus(ctx context.Context, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return r.store.findTasks(ctx, bson.M{"status": string(status)})
}
func (r taskRepo) FindByUserIDAndStatus(ctx context.Context, userID string, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return r.store.findTasks(ctx, bson.M{"user_id": userID, "status": string(status)})
}
func (r taskRepo) UpdateStatus(ctx context.Context, id string, status dao.ScheduledTaskStatus) error {
	_, err := r.store.collection("scheduled_tasks").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"status": string(status)}})
	return wrap("update scheduled task status", err)
}

type credentialRepo struct{ store *Store }

func (r credentialRepo) Save(ctx context.Context, value dao.ShellCredential) error {
	return r.store.saveCredential(ctx, value)
}
func (r credentialRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.ShellCredential, error) {
	return r.store.findCredentials(ctx, bson.M{"owner_id": ownerID})
}
func (r credentialRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.ShellCredential, error) {
	values, err := r.store.findCredentials(ctx, bson.M{"owner_id": ownerID, "name": name})
	if err != nil || len(values) == 0 {
		return nil, err
	}
	return &values[0], nil
}
func (r credentialRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	_, err := r.store.collection("shell_credentials").DeleteOne(ctx, bson.M{"owner_id": ownerID, "name": name})
	return wrap("delete shell credential", err)
}

type processedRepo struct{ store *Store }

func (r processedRepo) Claim(ctx context.Context, id string) (bool, error) {
	return r.store.claim(ctx, id)
}
func (r processedRepo) Release(ctx context.Context, id string) error { return r.store.release(ctx, id) }

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("mongodb store: %s: %w", operation, err)
}

var (
	_ dao.Backend           = (*Store)(nil)
	_ chatmemory.Repository = (*Store)(nil)
)

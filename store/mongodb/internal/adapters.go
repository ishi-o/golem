package mongodb

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/store"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

type mcpStore struct{ store *Store }

func (r mcpStore) Save(ctx context.Context, value store.MCPServerConfig) error {
	return r.store.saveMCP(ctx, value)
}
func (r mcpStore) ListByOwner(ctx context.Context, ownerID string) ([]store.MCPServerConfig, error) {
	return r.store.findMCPBy(ctx, bson.M{"owner_id": ownerID})
}
func (r mcpStore) GetByOwnerAndName(ctx context.Context, ownerID, name string) (*store.MCPServerConfig, error) {
	return r.store.findOneMCP(ctx, bson.M{"owner_id": ownerID, "name": name})
}
func (r mcpStore) ExistsByOwnerAndName(ctx context.Context, ownerID, name string) (bool, error) {
	count, err := r.store.collection("mcp_servers").CountDocuments(ctx, bson.M{"owner_id": ownerID, "name": name})
	return count > 0, wrap("check MCP server", err)
}
func (r mcpStore) DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error {
	_, err := r.store.collection("mcp_servers").DeleteOne(ctx, bson.M{"owner_id": ownerID, "name": name})
	return wrap("delete MCP server", err)
}
func (r mcpStore) ListSharedWith(ctx context.Context, identifiers []string) ([]store.MCPServerConfig, error) {
	if len(identifiers) == 0 {
		return []store.MCPServerConfig{}, nil
	}
	return r.store.findMCPBy(ctx, bson.M{"shared_with": bson.M{"$in": identifiers}})
}
func (r mcpStore) ListAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]store.MCPServerConfig, error) {
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

type pendingStore struct{ store *Store }

func (r pendingStore) Save(ctx context.Context, value store.PendingQuestion) error {
	return r.store.savePending(ctx, value)
}
func (r pendingStore) Get(ctx context.Context, id string) (*store.PendingQuestion, error) {
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
func (r pendingStore) ListByConversationAndStatus(ctx context.Context, conversationID string, status store.PendingQuestionStatus) ([]store.PendingQuestion, error) {
	return r.store.findPending(ctx, bson.M{"conversation_id": conversationID, "status": string(status)})
}
func (r pendingStore) SetStatus(ctx context.Context, id string, status store.PendingQuestionStatus) error {
	_, err := r.store.collection("pending_questions").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"status": string(status)}})
	return wrap("update pending question status", err)
}

type resourceStore struct{ store *Store }

func (r resourceStore) Save(ctx context.Context, value store.PublishedResource) error {
	return r.store.saveResource(ctx, value)
}
func (r resourceStore) Get(ctx context.Context, id string) (*store.PublishedResource, error) {
	return r.store.findResource(ctx, id)
}
func (r resourceStore) Delete(ctx context.Context, id string) error {
	_, err := r.store.collection("published_resources").DeleteOne(ctx, bson.M{"_id": id})
	return wrap("delete published resource", err)
}

type taskStore struct{ store *Store }

func (r taskStore) Save(ctx context.Context, value store.ScheduledTask) error {
	return r.store.saveTask(ctx, value)
}
func (r taskStore) Get(ctx context.Context, id string) (*store.ScheduledTask, error) {
	return r.store.findOneTask(ctx, id)
}
func (r taskStore) ListByStatus(ctx context.Context, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	return r.store.findTasks(ctx, bson.M{"status": string(status)})
}
func (r taskStore) ListByUserAndStatus(ctx context.Context, userID string, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	return r.store.findTasks(ctx, bson.M{"user_id": userID, "status": string(status)})
}
func (r taskStore) SetStatus(ctx context.Context, id string, status store.ScheduledTaskStatus) error {
	_, err := r.store.collection("scheduled_tasks").UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"status": string(status)}})
	return wrap("update scheduled task status", err)
}

type credentialStore struct{ store *Store }

func (r credentialStore) Save(ctx context.Context, value store.ShellCredential) error {
	return r.store.saveCredential(ctx, value)
}
func (r credentialStore) ListByOwner(ctx context.Context, ownerID string) ([]store.ShellCredential, error) {
	return r.store.findCredentials(ctx, bson.M{"owner_id": ownerID})
}
func (r credentialStore) GetByOwnerAndName(ctx context.Context, ownerID, name string) (*store.ShellCredential, error) {
	values, err := r.store.findCredentials(ctx, bson.M{"owner_id": ownerID, "name": name})
	if err != nil || len(values) == 0 {
		return nil, err
	}
	return &values[0], nil
}
func (r credentialStore) DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error {
	_, err := r.store.collection("shell_credentials").DeleteOne(ctx, bson.M{"owner_id": ownerID, "name": name})
	return wrap("delete shell credential", err)
}

type processedStore struct{ store *Store }

func (r processedStore) Claim(ctx context.Context, id string) (bool, error) {
	return r.store.claim(ctx, id)
}
func (r processedStore) Release(ctx context.Context, id string) error {
	return r.store.release(ctx, id)
}

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("mongodb store: %s: %w", operation, err)
}

var (
	_ store.Backend         = (*Store)(nil)
	_ chatmemory.Repository = (*Store)(nil)
)

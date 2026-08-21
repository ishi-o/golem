package mongostore

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
)

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

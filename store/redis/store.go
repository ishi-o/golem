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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
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

type mcpDocument struct {
	ID          string            `json:"id"`
	OwnerID     string            `json:"ownerId"`
	Name        string            `json:"name"`
	Transport   dao.McpTransport  `json:"transport"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	Title       string            `json:"title,omitempty"`
	Version     string            `json:"version,omitempty"`
	Description string            `json:"description,omitempty"`
	WebsiteURL  string            `json:"websiteUrl,omitempty"`
	Enabled     bool              `json:"enabled"`
	SharedWith  []string          `json:"sharedWith,omitempty"`
}

func (s *Store) McpServerConfigs() dao.McpServerConfigRepo { return mcpRepo{s} }

func (s *Store) saveMCP(ctx context.Context, value dao.McpServerConfig) error {
	var old mcpDocument
	hasOld, err := s.get(ctx, s.record("mcp", value.ID), &old)
	if err != nil {
		return err
	}
	document := mcpDocument{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Transport: value.Transport, URL: value.URL, Headers: value.Headers, Title: value.Title, Version: value.Version, Description: value.Description, WebsiteURL: value.WebsiteURL, Enabled: value.Enabled, SharedWith: value.SharedWith}
	return s.tx(ctx, func(pipe redis.Pipeliner) error {
		if hasOld {
			pipe.SRem(ctx, s.index("mcp-owner", old.OwnerID), value.ID)
			pipe.SRem(ctx, s.index("mcp-name", old.OwnerID+"\x00"+old.Name), value.ID)
			for _, identifier := range old.SharedWith {
				pipe.SRem(ctx, s.index("mcp-shared", identifier), value.ID)
			}
		}
		if err := s.setJSON(ctx, pipe, s.record("mcp", value.ID), document); err != nil {
			return err
		}
		pipe.SAdd(ctx, s.all("mcp"), value.ID)
		pipe.SAdd(ctx, s.index("mcp-owner", value.OwnerID), value.ID)
		pipe.SAdd(ctx, s.index("mcp-name", value.OwnerID+"\x00"+value.Name), value.ID)
		for _, identifier := range value.SharedWith {
			pipe.SAdd(ctx, s.index("mcp-shared", identifier), value.ID)
		}
		return nil
	})
}

func (s *Store) findMCPIDs(ctx context.Context, key string) ([]string, error) {
	ids, err := s.client.SMembers(ctx, key).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: read MCP index: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) findMCPByIDs(ctx context.Context, ids []string) ([]dao.McpServerConfig, error) {
	out := make([]dao.McpServerConfig, 0, len(ids))
	for _, id := range ids {
		var document mcpDocument
		found, err := s.get(ctx, s.record("mcp", id), &document)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, dao.McpServerConfig{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Transport: document.Transport, URL: document.URL, Headers: document.Headers, Title: document.Title, Version: document.Version, Description: document.Description, WebsiteURL: document.WebsiteURL, Enabled: document.Enabled, SharedWith: document.SharedWith})
	}
	return out, nil
}

type pendingDocument struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId,omitempty"`
	ChatID         string    `json:"chatId,omitempty"`
	ChatType       string    `json:"chatType,omitempty"`
	ConversationID string    `json:"conversationId,omitempty"`
	RootMessageID  string    `json:"rootMessageId,omitempty"`
	CardID         string    `json:"cardId,omitempty"`
	QuestionsJSON  string    `json:"questionsJson,omitempty"`
	Status         string    `json:"status"`
	CreatedAt      time.Time `json:"createdAt,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt,omitempty"`
}

func (s *Store) PendingQuestions() dao.PendingQuestionRepo { return pendingRepo{s} }

func (s *Store) savePending(ctx context.Context, value dao.PendingQuestion) error {
	var old pendingDocument
	hasOld, err := s.get(ctx, s.record("pending", value.ID), &old)
	if err != nil {
		return err
	}
	document := pendingDocument{ID: value.ID, UserID: value.UserID, ChatID: value.ChatID, ChatType: value.ChatType, ConversationID: value.ConversationID, RootMessageID: value.RootMessageID, CardID: value.CardID, QuestionsJSON: value.QuestionsJSON, Status: string(value.Status), CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt}
	return s.tx(ctx, func(pipe redis.Pipeliner) error {
		if hasOld {
			pipe.SRem(ctx, s.index("pending-conversation-status", old.ConversationID+"\x00"+old.Status), value.ID)
		}
		if err := s.setJSON(ctx, pipe, s.record("pending", value.ID), document); err != nil {
			return err
		}
		pipe.SAdd(ctx, s.all("pending"), value.ID)
		pipe.SAdd(ctx, s.index("pending-conversation-status", value.ConversationID+"\x00"+string(value.Status)), value.ID)
		return nil
	})
}

func (s *Store) findPendingIDs(ctx context.Context, conversationID string, status dao.PendingQuestionStatus) ([]string, error) {
	ids, err := s.client.SMembers(ctx, s.index("pending-conversation-status", conversationID+"\x00"+string(status))).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: read pending index: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) getPending(ctx context.Context, id string) (*dao.PendingQuestion, error) {
	var document pendingDocument
	found, err := s.get(ctx, s.record("pending", id), &document)
	if err != nil || !found {
		return nil, err
	}
	value := pendingValue(document)
	return &value, nil
}

func pendingValue(document pendingDocument) dao.PendingQuestion {
	return dao.PendingQuestion{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, ConversationID: document.ConversationID, RootMessageID: document.RootMessageID, CardID: document.CardID, QuestionsJSON: document.QuestionsJSON, Status: dao.PendingQuestionStatus(document.Status), CreatedAt: document.CreatedAt, ExpiresAt: document.ExpiresAt}
}

type resourceDocument struct {
	ID            string    `json:"id"`
	OwnerID       string    `json:"ownerId,omitempty"`
	Visibility    string    `json:"visibility,omitempty"`
	Directory     bool      `json:"directory"`
	EntryFilename string    `json:"entryFilename,omitempty"`
	ExpiresAt     time.Time `json:"expiresAt,omitempty"`
}

func (s *Store) PublishedResources() dao.PublishedResourceRepo { return resourceRepo{s} }

func (s *Store) saveResource(ctx context.Context, value dao.PublishedResource) error {
	document := resourceDocument{ID: value.ID, OwnerID: value.OwnerID, Visibility: string(value.Visibility), Directory: value.Directory, EntryFilename: value.EntryFilename, ExpiresAt: value.ExpiresAt}
	return s.setJSONDirect(ctx, s.record("resource", value.ID), document)
}

func (s *Store) findResource(ctx context.Context, id string) (*dao.PublishedResource, error) {
	var document resourceDocument
	found, err := s.get(ctx, s.record("resource", id), &document)
	if err != nil || !found {
		return nil, err
	}
	return &dao.PublishedResource{ID: document.ID, OwnerID: document.OwnerID, Visibility: dao.Visibility(document.Visibility), Directory: document.Directory, EntryFilename: document.EntryFilename, ExpiresAt: document.ExpiresAt}, nil
}

type taskDocument struct {
	ID             string    `json:"id"`
	UserID         string    `json:"userId,omitempty"`
	ChatID         string    `json:"chatId,omitempty"`
	ChatType       string    `json:"chatType,omitempty"`
	RootMessageID  string    `json:"rootMessageId,omitempty"`
	TaskText       string    `json:"taskText,omitempty"`
	CronExpression string    `json:"cronExpression,omitempty"`
	ScheduledAt    time.Time `json:"scheduledAt,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt,omitempty"`
	Background     bool      `json:"background"`
	Status         string    `json:"status"`
}

func (s *Store) ScheduledTasks() dao.ScheduledTaskRepo { return taskRepo{s} }

func (s *Store) saveTask(ctx context.Context, value dao.ScheduledTask) error {
	var old taskDocument
	hasOld, err := s.get(ctx, s.record("task", value.ID), &old)
	if err != nil {
		return err
	}
	document := taskDocument{ID: value.ID, UserID: value.UserID, ChatID: value.ChatID, ChatType: value.ChatType, RootMessageID: value.RootMessageID, TaskText: value.TaskText, CronExpression: value.CronExpression, ScheduledAt: value.ScheduledAt, ExpiresAt: value.ExpiresAt, Background: value.Background, Status: string(value.Status)}
	return s.tx(ctx, func(pipe redis.Pipeliner) error {
		if hasOld {
			pipe.SRem(ctx, s.index("task-status", old.Status), value.ID)
			pipe.SRem(ctx, s.index("task-user-status", old.UserID+"\x00"+old.Status), value.ID)
		}
		if err := s.setJSON(ctx, pipe, s.record("task", value.ID), document); err != nil {
			return err
		}
		pipe.SAdd(ctx, s.all("task"), value.ID)
		pipe.SAdd(ctx, s.index("task-status", string(value.Status)), value.ID)
		pipe.SAdd(ctx, s.index("task-user-status", value.UserID+"\x00"+string(value.Status)), value.ID)
		return nil
	})
}

func (s *Store) getTasks(ctx context.Context, ids []string) ([]dao.ScheduledTask, error) {
	out := make([]dao.ScheduledTask, 0, len(ids))
	for _, id := range ids {
		var document taskDocument
		found, err := s.get(ctx, s.record("task", id), &document)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, dao.ScheduledTask{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, RootMessageID: document.RootMessageID, TaskText: document.TaskText, CronExpression: document.CronExpression, ScheduledAt: document.ScheduledAt, ExpiresAt: document.ExpiresAt, Background: document.Background, Status: dao.ScheduledTaskStatus(document.Status)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type credentialDocument struct {
	ID      string `json:"id"`
	OwnerID string `json:"ownerId"`
	Name    string `json:"name"`
	Value   string `json:"value,omitempty"`
}

func (s *Store) ShellCredentials() dao.ShellCredentialRepo { return credentialRepo{s} }

func (s *Store) saveCredential(ctx context.Context, value dao.ShellCredential) error {
	value.ID = dao.ShellCredentialID(value.OwnerID, value.Name)
	document := credentialDocument{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Value: value.Value}
	return s.setJSONDirect(ctx, s.record("credential", value.ID), document)
}

func (s *Store) findCredentials(ctx context.Context, ownerID string) ([]dao.ShellCredential, error) {
	ids, err := s.client.SMembers(ctx, s.index("credential-owner", ownerID)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: read credential index: %w", err)
	}
	sort.Strings(ids)
	out := make([]dao.ShellCredential, 0, len(ids))
	for _, id := range ids {
		var document credentialDocument
		found, err := s.get(ctx, s.record("credential", id), &document)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, dao.ShellCredential{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Value: document.Value})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) ProcessedMessages() dao.ProcessedMessageRepo { return processedRepo{s} }

func (s *Store) claim(ctx context.Context, id string) (bool, error) {
	ok, err := s.client.SetNX(ctx, s.record("processed", id), time.Now().UTC().Format(time.RFC3339Nano), 0).Result()
	return ok, wrap("claim processed message", err)
}

func (s *Store) release(ctx context.Context, id string) error {
	return wrap("release processed message", s.client.Del(ctx, s.record("processed", id)).Err())
}

func (s *Store) Append(ctx context.Context, conversationID string, messages []*schema.Message) error {
	if conversationID == "" || len(messages) == 0 {
		return nil
	}
	encodedMessages := make([]any, 0, len(messages))
	for _, message := range messages {
		data, err := json.Marshal(message)
		if err != nil {
			return fmt.Errorf("encode chat message: %w", err)
		}
		encodedMessages = append(encodedMessages, string(data))
	}
	pipe := s.client.TxPipeline()
	pipe.RPush(ctx, s.record("memory", conversationID), encodedMessages...)
	pipe.SAdd(ctx, s.all("memory"), conversationID)
	_, err := pipe.Exec(ctx)
	return wrap("append chat memory", err)
}

func (s *Store) Load(ctx context.Context, conversationID string, window int) ([]*schema.Message, error) {
	start := int64(0)
	if window > 0 {
		start = -int64(window)
	}
	values, err := s.client.LRange(ctx, s.record("memory", conversationID), start, -1).Result()
	if err != nil {
		return nil, wrap("load chat memory", err)
	}
	out := make([]*schema.Message, 0, len(values))
	for _, value := range values {
		var message schema.Message
		if err := json.Unmarshal([]byte(value), &message); err != nil {
			return nil, fmt.Errorf("decode chat message: %w", err)
		}
		out = append(out, &message)
	}
	return out, nil
}

func (s *Store) Delete(ctx context.Context, conversationID string) error {
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, s.record("memory", conversationID))
	pipe.SRem(ctx, s.all("memory"), conversationID)
	_, err := pipe.Exec(ctx)
	return wrap("delete chat memory", err)
}

func (s *Store) ListConversations(ctx context.Context) ([]string, error) {
	ids, err := s.client.SMembers(ctx, s.all("memory")).Result()
	if err != nil {
		return nil, wrap("list chat conversations", err)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) updatePendingStatus(ctx context.Context, id string, status dao.PendingQuestionStatus) error {
	key := s.record("pending", id)
	err := s.watch(ctx, key, func(tx *redis.Tx) error {
		data, err := tx.Get(ctx, key).Bytes()
		if errors.Is(err, redis.Nil) {
			return nil
		}
		if err != nil {
			return err
		}
		var old pendingDocument
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("decode pending question: %w", err)
		}
		if old.Status == string(status) {
			return nil
		}
		previousStatus := old.Status
		old.Status = string(status)
		pipe := tx.TxPipeline()
		pipe.SRem(ctx, s.index("pending-conversation-status", old.ConversationID+"\x00"+previousStatus), id)
		if err := s.setJSON(ctx, pipe, key, old); err != nil {
			return err
		}
		pipe.SAdd(ctx, s.index("pending-conversation-status", old.ConversationID+"\x00"+old.Status), id)
		_, err = pipe.Exec(ctx)
		return err
	})
	return wrap("update pending question status", err)
}

func (s *Store) updateTaskStatus(ctx context.Context, id string, status dao.ScheduledTaskStatus) error {
	key := s.record("task", id)
	err := s.watch(ctx, key, func(tx *redis.Tx) error {
		data, err := tx.Get(ctx, key).Bytes()
		if errors.Is(err, redis.Nil) {
			return nil
		}
		if err != nil {
			return err
		}
		var old taskDocument
		if err := json.Unmarshal(data, &old); err != nil {
			return fmt.Errorf("decode scheduled task: %w", err)
		}
		if old.Status == string(status) {
			return nil
		}
		previousStatus := old.Status
		old.Status = string(status)
		pipe := tx.TxPipeline()
		pipe.SRem(ctx, s.index("task-status", previousStatus), id)
		pipe.SRem(ctx, s.index("task-user-status", old.UserID+"\x00"+previousStatus), id)
		if err := s.setJSON(ctx, pipe, key, old); err != nil {
			return err
		}
		pipe.SAdd(ctx, s.index("task-status", old.Status), id)
		pipe.SAdd(ctx, s.index("task-user-status", old.UserID+"\x00"+old.Status), id)
		_, err = pipe.Exec(ctx)
		return err
	})
	return wrap("update scheduled task status", err)
}

func (s *Store) watch(ctx context.Context, key string, fn func(*redis.Tx) error) error {
	for attempt := 0; attempt < 5; attempt++ {
		err := s.client.Watch(ctx, fn, key)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Millisecond):
		}
	}
	return redis.TxFailedErr
}

func (s *Store) deleteCredential(ctx context.Context, ownerID, name string) error {
	id := dao.ShellCredentialID(ownerID, name)
	pipe := s.client.TxPipeline()
	pipe.Del(ctx, s.record("credential", id))
	pipe.SRem(ctx, s.all("credential"), id)
	pipe.SRem(ctx, s.index("credential-owner", ownerID), id)
	pipe.SRem(ctx, s.index("credential-name", ownerID+"\x00"+name), id)
	_, err := pipe.Exec(ctx)
	return wrap("delete shell credential", err)
}

func (s *Store) setJSONDirect(ctx context.Context, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("redis store: encode record: %w", err)
	}
	return wrap("write record", s.client.Set(ctx, key, data, 0).Err())
}

func (s *Store) setJSON(ctx context.Context, pipe redis.Pipeliner, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("redis store: encode record: %w", err)
	}
	pipe.Set(ctx, key, data, 0)
	return nil
}

func (s *Store) get(ctx context.Context, key string, target any) (bool, error) {
	data, err := s.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("redis store: read record: %w", err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return false, fmt.Errorf("redis store: decode record: %w", err)
	}
	return true, nil
}

func (s *Store) tx(ctx context.Context, fn func(redis.Pipeliner) error) error {
	_, err := s.client.TxPipelined(ctx, fn)
	return wrap("commit index update", err)
}

type mcpRepo struct{ store *Store }

func (r mcpRepo) Save(ctx context.Context, value dao.McpServerConfig) error {
	return r.store.saveMCP(ctx, value)
}
func (r mcpRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.McpServerConfig, error) {
	ids, err := r.store.findMCPIDs(ctx, r.store.index("mcp-owner", ownerID))
	if err != nil {
		return nil, err
	}
	return r.store.findMCPByIDs(ctx, ids)
}
func (r mcpRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.McpServerConfig, error) {
	ids, err := r.store.findMCPIDs(ctx, r.store.index("mcp-name", ownerID+"\x00"+name))
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	values, err := r.store.findMCPByIDs(ctx, ids[:1])
	if err != nil || len(values) == 0 {
		return nil, err
	}
	return &values[0], nil
}
func (r mcpRepo) ExistsByOwnerIDAndName(ctx context.Context, ownerID, name string) (bool, error) {
	ids, err := r.store.findMCPIDs(ctx, r.store.index("mcp-name", ownerID+"\x00"+name))
	return len(ids) > 0, err
}
func (r mcpRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	ids, err := r.store.findMCPIDs(ctx, r.store.index("mcp-name", ownerID+"\x00"+name))
	if err != nil {
		return err
	}
	for _, id := range ids {
		var value mcpDocument
		found, err := r.store.get(ctx, r.store.record("mcp", id), &value)
		if err != nil || !found {
			continue
		}
		pipe := r.store.client.TxPipeline()
		pipe.Del(ctx, r.store.record("mcp", id))
		pipe.SRem(ctx, r.store.all("mcp"), id)
		pipe.SRem(ctx, r.store.index("mcp-owner", value.OwnerID), id)
		pipe.SRem(ctx, r.store.index("mcp-name", value.OwnerID+"\x00"+value.Name), id)
		for _, shared := range value.SharedWith {
			pipe.SRem(ctx, r.store.index("mcp-shared", shared), id)
		}
		if _, err := pipe.Exec(ctx); err != nil {
			return wrap("delete MCP server", err)
		}
	}
	return nil
}
func (r mcpRepo) FindBySharedWithIn(ctx context.Context, identifiers []string) ([]dao.McpServerConfig, error) {
	seen := map[string]struct{}{}
	for _, identifier := range identifiers {
		ids, err := r.store.findMCPIDs(ctx, r.store.index("mcp-shared", identifier))
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			seen[id] = struct{}{}
		}
	}
	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return r.store.findMCPByIDs(ctx, ids)
}
func (r mcpRepo) FindAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]dao.McpServerConfig, error) {
	owned, err := r.FindByOwnerID(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	shared, err := r.FindBySharedWithIn(ctx, identifiers)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]dao.McpServerConfig, 0, len(owned)+len(shared))
	for _, value := range append(owned, shared...) {
		if _, ok := seen[value.ID]; !ok {
			seen[value.ID] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type pendingRepo struct{ store *Store }

func (r pendingRepo) Save(ctx context.Context, value dao.PendingQuestion) error {
	return r.store.savePending(ctx, value)
}
func (r pendingRepo) FindByID(ctx context.Context, id string) (*dao.PendingQuestion, error) {
	return r.store.getPending(ctx, id)
}
func (r pendingRepo) FindByConversationIDAndStatus(ctx context.Context, conversationID string, status dao.PendingQuestionStatus) ([]dao.PendingQuestion, error) {
	ids, err := r.store.findPendingIDs(ctx, conversationID, status)
	if err != nil {
		return nil, err
	}
	out := make([]dao.PendingQuestion, 0, len(ids))
	for _, id := range ids {
		value, err := r.store.getPending(ctx, id)
		if err != nil {
			return nil, err
		}
		if value != nil {
			out = append(out, *value)
		}
	}
	return out, nil
}
func (r pendingRepo) UpdateStatus(ctx context.Context, id string, status dao.PendingQuestionStatus) error {
	return r.store.updatePendingStatus(ctx, id, status)
}

type resourceRepo struct{ store *Store }

func (r resourceRepo) Save(ctx context.Context, value dao.PublishedResource) error {
	return r.store.saveResource(ctx, value)
}
func (r resourceRepo) FindByID(ctx context.Context, id string) (*dao.PublishedResource, error) {
	return r.store.findResource(ctx, id)
}
func (r resourceRepo) DeleteByID(ctx context.Context, id string) error {
	return wrap("delete published resource", r.store.client.Del(ctx, r.store.record("resource", id)).Err())
}

type taskRepo struct{ store *Store }

func (r taskRepo) Save(ctx context.Context, value dao.ScheduledTask) error {
	return r.store.saveTask(ctx, value)
}
func (r taskRepo) FindByID(ctx context.Context, id string) (*dao.ScheduledTask, error) {
	var document taskDocument
	found, err := r.store.get(ctx, r.store.record("task", id), &document)
	if err != nil || !found {
		return nil, err
	}
	value := dao.ScheduledTask{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, RootMessageID: document.RootMessageID, TaskText: document.TaskText, CronExpression: document.CronExpression, ScheduledAt: document.ScheduledAt, ExpiresAt: document.ExpiresAt, Background: document.Background, Status: dao.ScheduledTaskStatus(document.Status)}
	return &value, nil
}
func (r taskRepo) FindByStatus(ctx context.Context, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	ids, err := r.store.client.SMembers(ctx, r.store.index("task-status", string(status))).Result()
	if err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	return r.store.getTasks(ctx, ids)
}
func (r taskRepo) FindByUserIDAndStatus(ctx context.Context, userID string, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	ids, err := r.store.client.SMembers(ctx, r.store.index("task-user-status", userID+"\x00"+string(status))).Result()
	if err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	return r.store.getTasks(ctx, ids)
}
func (r taskRepo) UpdateStatus(ctx context.Context, id string, status dao.ScheduledTaskStatus) error {
	return r.store.updateTaskStatus(ctx, id, status)
}

type credentialRepo struct{ store *Store }

func (r credentialRepo) Save(ctx context.Context, value dao.ShellCredential) error {
	value.ID = dao.ShellCredentialID(value.OwnerID, value.Name)
	if err := r.store.saveCredential(ctx, value); err != nil {
		return err
	}
	pipe := r.store.client.TxPipeline()
	pipe.SAdd(ctx, r.store.all("credential"), value.ID)
	pipe.SAdd(ctx, r.store.index("credential-owner", value.OwnerID), value.ID)
	pipe.SAdd(ctx, r.store.index("credential-name", value.OwnerID+"\x00"+value.Name), value.ID)
	_, err := pipe.Exec(ctx)
	return wrap("index shell credential", err)
}
func (r credentialRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.ShellCredential, error) {
	return r.store.findCredentials(ctx, ownerID)
}
func (r credentialRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.ShellCredential, error) {
	id := dao.ShellCredentialID(ownerID, name)
	var value credentialDocument
	found, err := r.store.get(ctx, r.store.record("credential", id), &value)
	if err != nil || !found {
		return nil, err
	}
	result := dao.ShellCredential{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Value: value.Value}
	return &result, nil
}
func (r credentialRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	return r.store.deleteCredential(ctx, ownerID, name)
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
	return fmt.Errorf("redis store: %s: %w", operation, err)
}

var (
	_ dao.Backend           = (*Store)(nil)
	_ chatmemory.Repository = (*Store)(nil)
)

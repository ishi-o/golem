package redisstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
	"github.com/redis/go-redis/v9"
)

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

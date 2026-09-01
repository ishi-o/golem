package redis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/store"
	"github.com/redis/go-redis/v9"
)

func (s *Store) updatePendingStatus(ctx context.Context, id string, status store.PendingQuestionStatus) error {
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

func (s *Store) updateTaskStatus(ctx context.Context, id string, status store.ScheduledTaskStatus) error {
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
	id := store.ShellCredentialID(ownerID, name)
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

type mcpStore struct{ store *Store }

func (r mcpStore) Save(ctx context.Context, value store.MCPServerConfig) error {
	return r.store.saveMCP(ctx, value)
}
func (r mcpStore) ListByOwner(ctx context.Context, ownerID string) ([]store.MCPServerConfig, error) {
	ids, err := r.store.findMCPIDs(ctx, r.store.index("mcp-owner", ownerID))
	if err != nil {
		return nil, err
	}
	return r.store.findMCPByIDs(ctx, ids)
}
func (r mcpStore) GetByOwnerAndName(ctx context.Context, ownerID, name string) (*store.MCPServerConfig, error) {
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
func (r mcpStore) ExistsByOwnerAndName(ctx context.Context, ownerID, name string) (bool, error) {
	ids, err := r.store.findMCPIDs(ctx, r.store.index("mcp-name", ownerID+"\x00"+name))
	return len(ids) > 0, err
}
func (r mcpStore) DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error {
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
func (r mcpStore) ListSharedWith(ctx context.Context, identifiers []string) ([]store.MCPServerConfig, error) {
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
func (r mcpStore) ListAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]store.MCPServerConfig, error) {
	owned, err := r.ListByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	shared, err := r.ListSharedWith(ctx, identifiers)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	out := make([]store.MCPServerConfig, 0, len(owned)+len(shared))
	for _, value := range append(owned, shared...) {
		if _, ok := seen[value.ID]; !ok {
			seen[value.ID] = struct{}{}
			out = append(out, value)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

type pendingStore struct{ store *Store }

func (r pendingStore) Save(ctx context.Context, value store.PendingQuestion) error {
	return r.store.savePending(ctx, value)
}
func (r pendingStore) Get(ctx context.Context, id string) (*store.PendingQuestion, error) {
	return r.store.getPending(ctx, id)
}
func (r pendingStore) ListByConversationAndStatus(ctx context.Context, conversationID string, status store.PendingQuestionStatus) ([]store.PendingQuestion, error) {
	ids, err := r.store.findPendingIDs(ctx, conversationID, status)
	if err != nil {
		return nil, err
	}
	out := make([]store.PendingQuestion, 0, len(ids))
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
func (r pendingStore) SetStatus(ctx context.Context, id string, status store.PendingQuestionStatus) error {
	return r.store.updatePendingStatus(ctx, id, status)
}

type resourceStore struct{ store *Store }

func (r resourceStore) Save(ctx context.Context, value store.PublishedResource) error {
	return r.store.saveResource(ctx, value)
}
func (r resourceStore) Get(ctx context.Context, id string) (*store.PublishedResource, error) {
	return r.store.findResource(ctx, id)
}
func (r resourceStore) Delete(ctx context.Context, id string) error {
	return wrap("delete published resource", r.store.client.Del(ctx, r.store.record("resource", id)).Err())
}

type taskStore struct{ store *Store }

func (r taskStore) Save(ctx context.Context, value store.ScheduledTask) error {
	return r.store.saveTask(ctx, value)
}
func (r taskStore) Get(ctx context.Context, id string) (*store.ScheduledTask, error) {
	var document taskDocument
	found, err := r.store.get(ctx, r.store.record("task", id), &document)
	if err != nil || !found {
		return nil, err
	}
	value := store.ScheduledTask{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, RootMessageID: document.RootMessageID, ConversationID: document.ConversationID, GroupID: document.GroupID, TenantID: document.TenantID, TaskText: document.TaskText, CronExpression: document.CronExpression, ScheduledAt: document.ScheduledAt, ExpiresAt: document.ExpiresAt, NextFireAt: document.NextFireAt, MaxRuns: document.MaxRuns, RunCount: document.RunCount, Background: document.Background, Status: store.ScheduledTaskStatus(document.Status)}
	return &value, nil
}
func (r taskStore) ListByStatus(ctx context.Context, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	ids, err := r.store.client.SMembers(ctx, r.store.index("task-status", string(status))).Result()
	if err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	return r.store.getTasks(ctx, ids)
}
func (r taskStore) ListByUserAndStatus(ctx context.Context, userID string, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	ids, err := r.store.client.SMembers(ctx, r.store.index("task-user-status", userID+"\x00"+string(status))).Result()
	if err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	return r.store.getTasks(ctx, ids)
}
func (r taskStore) SetStatus(ctx context.Context, id string, status store.ScheduledTaskStatus) error {
	return r.store.updateTaskStatus(ctx, id, status)
}

type credentialStore struct{ store *Store }

func (r credentialStore) Save(ctx context.Context, value store.ShellCredential) error {
	value.ID = store.ShellCredentialID(value.OwnerID, value.Name)
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
func (r credentialStore) ListByOwner(ctx context.Context, ownerID string) ([]store.ShellCredential, error) {
	return r.store.findCredentials(ctx, ownerID)
}
func (r credentialStore) GetByOwnerAndName(ctx context.Context, ownerID, name string) (*store.ShellCredential, error) {
	id := store.ShellCredentialID(ownerID, name)
	var value credentialDocument
	found, err := r.store.get(ctx, r.store.record("credential", id), &value)
	if err != nil || !found {
		return nil, err
	}
	result := store.ShellCredential{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Value: value.Value}
	return &result, nil
}
func (r credentialStore) DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error {
	return r.store.deleteCredential(ctx, ownerID, name)
}

type processedStore struct{ store *Store }

func (r processedStore) Claim(ctx context.Context, id string) (bool, error) {
	return r.store.claim(ctx, id)
}
func (r processedStore) Release(ctx context.Context, id string) error {
	return r.store.release(ctx, id)
}

type observedEventStore struct{ store *Store }

func (r observedEventStore) Save(ctx context.Context, value store.ObservedEvent) error {
	return r.store.saveObservedEvent(ctx, value)
}

func (r observedEventStore) ListBySituation(ctx context.Context, situationID string) ([]store.ObservedEvent, error) {
	return r.store.findObservedEvents(ctx, situationID)
}

type situationStore struct{ store *Store }

func (r situationStore) Save(ctx context.Context, value store.Situation) error {
	return r.store.saveSituation(ctx, value)
}

func (r situationStore) Get(ctx context.Context, id string) (*store.Situation, error) {
	var document situationDocument
	found, err := r.store.get(ctx, r.store.record("situation", id), &document)
	if err != nil || !found {
		return nil, err
	}
	value := situationValue(document)
	return &value, nil
}

func (r situationStore) ListByCorrelationAndStatus(ctx context.Context, correlationKey string, status store.SituationStatus) ([]store.Situation, error) {
	return r.store.findSituationsByIndex(ctx, "situation-correlation-status", correlationKey+"\x00"+string(status))
}

func (r situationStore) ListBySourceAndCorrelationAndStatus(ctx context.Context, source, correlationKey string, status store.SituationStatus) ([]store.Situation, error) {
	return r.store.findSituationsByIndex(ctx, "situation-source-correlation-status", source+"\x00"+correlationKey+"\x00"+string(status))
}

func (r situationStore) ListByStatus(ctx context.Context, status store.SituationStatus) ([]store.Situation, error) {
	return r.store.findSituationsByIndex(ctx, "situation-status", string(status))
}

func (r situationStore) ListByPhase(ctx context.Context, phase store.SituationPhase) ([]store.Situation, error) {
	return r.store.findSituationsByIndex(ctx, "situation-phase", string(phase))
}

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("redis store: %s: %w", operation, err)
}

var (
	_ store.Backend         = (*Store)(nil)
	_ chatmemory.Repository = (*Store)(nil)
)

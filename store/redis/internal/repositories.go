package redis

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ishi-o/golem/core/store"
	"github.com/redis/go-redis/v9"
)

type mcpDocument struct {
	ID          string             `json:"id"`
	OwnerID     string             `json:"ownerId"`
	Name        string             `json:"name"`
	Transport   store.MCPTransport `json:"transport"`
	URL         string             `json:"url"`
	Headers     map[string]string  `json:"headers,omitempty"`
	Title       string             `json:"title,omitempty"`
	Version     string             `json:"version,omitempty"`
	Description string             `json:"description,omitempty"`
	WebsiteURL  string             `json:"websiteUrl,omitempty"`
	Enabled     bool               `json:"enabled"`
	SharedWith  []string           `json:"sharedWith,omitempty"`
}

func (s *Store) MCPServerConfigs() store.MCPServerConfigStore { return mcpStore{s} }

func (s *Store) saveMCP(ctx context.Context, value store.MCPServerConfig) error {
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

func (s *Store) findMCPByIDs(ctx context.Context, ids []string) ([]store.MCPServerConfig, error) {
	out := make([]store.MCPServerConfig, 0, len(ids))
	for _, id := range ids {
		var document mcpDocument
		found, err := s.get(ctx, s.record("mcp", id), &document)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		out = append(out, store.MCPServerConfig{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Transport: document.Transport, URL: document.URL, Headers: document.Headers, Title: document.Title, Version: document.Version, Description: document.Description, WebsiteURL: document.WebsiteURL, Enabled: document.Enabled, SharedWith: document.SharedWith})
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

func (s *Store) PendingQuestions() store.PendingQuestionStore { return pendingStore{s} }

func (s *Store) savePending(ctx context.Context, value store.PendingQuestion) error {
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

func (s *Store) findPendingIDs(ctx context.Context, conversationID string, status store.PendingQuestionStatus) ([]string, error) {
	ids, err := s.client.SMembers(ctx, s.index("pending-conversation-status", conversationID+"\x00"+string(status))).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: read pending index: %w", err)
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *Store) getPending(ctx context.Context, id string) (*store.PendingQuestion, error) {
	var document pendingDocument
	found, err := s.get(ctx, s.record("pending", id), &document)
	if err != nil || !found {
		return nil, err
	}
	value := pendingValue(document)
	return &value, nil
}

func pendingValue(document pendingDocument) store.PendingQuestion {
	return store.PendingQuestion{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, ConversationID: document.ConversationID, RootMessageID: document.RootMessageID, CardID: document.CardID, QuestionsJSON: document.QuestionsJSON, Status: store.PendingQuestionStatus(document.Status), CreatedAt: document.CreatedAt, ExpiresAt: document.ExpiresAt}
}

type resourceDocument struct {
	ID            string    `json:"id"`
	OwnerID       string    `json:"ownerId,omitempty"`
	Visibility    string    `json:"visibility,omitempty"`
	Directory     bool      `json:"directory"`
	EntryFilename string    `json:"entryFilename,omitempty"`
	ExpiresAt     time.Time `json:"expiresAt,omitempty"`
}

func (s *Store) PublishedResources() store.PublishedResourceStore { return resourceStore{s} }

func (s *Store) saveResource(ctx context.Context, value store.PublishedResource) error {
	document := resourceDocument{ID: value.ID, OwnerID: value.OwnerID, Visibility: string(value.Visibility), Directory: value.Directory, EntryFilename: value.EntryFilename, ExpiresAt: value.ExpiresAt}
	return s.setJSONDirect(ctx, s.record("resource", value.ID), document)
}

func (s *Store) findResource(ctx context.Context, id string) (*store.PublishedResource, error) {
	var document resourceDocument
	found, err := s.get(ctx, s.record("resource", id), &document)
	if err != nil || !found {
		return nil, err
	}
	return &store.PublishedResource{ID: document.ID, OwnerID: document.OwnerID, Visibility: store.Visibility(document.Visibility), Directory: document.Directory, EntryFilename: document.EntryFilename, ExpiresAt: document.ExpiresAt}, nil
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

func (s *Store) ScheduledTasks() store.ScheduledTaskStore { return taskStore{s} }

func (s *Store) saveTask(ctx context.Context, value store.ScheduledTask) error {
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

func (s *Store) getTasks(ctx context.Context, ids []string) ([]store.ScheduledTask, error) {
	out := make([]store.ScheduledTask, 0, len(ids))
	for _, id := range ids {
		var document taskDocument
		found, err := s.get(ctx, s.record("task", id), &document)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, store.ScheduledTask{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, RootMessageID: document.RootMessageID, TaskText: document.TaskText, CronExpression: document.CronExpression, ScheduledAt: document.ScheduledAt, ExpiresAt: document.ExpiresAt, Background: document.Background, Status: store.ScheduledTaskStatus(document.Status)})
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

func (s *Store) ShellCredentials() store.ShellCredentialStore { return credentialStore{s} }

func (s *Store) saveCredential(ctx context.Context, value store.ShellCredential) error {
	value.ID = store.ShellCredentialID(value.OwnerID, value.Name)
	document := credentialDocument{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Value: value.Value}
	return s.setJSONDirect(ctx, s.record("credential", value.ID), document)
}

func (s *Store) findCredentials(ctx context.Context, ownerID string) ([]store.ShellCredential, error) {
	ids, err := s.client.SMembers(ctx, s.index("credential-owner", ownerID)).Result()
	if err != nil {
		return nil, fmt.Errorf("redis store: read credential index: %w", err)
	}
	sort.Strings(ids)
	out := make([]store.ShellCredential, 0, len(ids))
	for _, id := range ids {
		var document credentialDocument
		found, err := s.get(ctx, s.record("credential", id), &document)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, store.ShellCredential{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Value: document.Value})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s *Store) ProcessedMessages() store.ProcessedMessageStore { return processedStore{s} }

func (s *Store) claim(ctx context.Context, id string) (bool, error) {
	ok, err := s.client.SetNX(ctx, s.record("processed", id), time.Now().UTC().Format(time.RFC3339Nano), 0).Result()
	return ok, wrap("claim processed message", err)
}

func (s *Store) release(ctx context.Context, id string) error {
	return wrap("release processed message", s.client.Del(ctx, s.record("processed", id)).Err())
}

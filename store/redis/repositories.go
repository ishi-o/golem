package redisstore

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/ishi-o/golem/core/dao"
	"github.com/redis/go-redis/v9"
)

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

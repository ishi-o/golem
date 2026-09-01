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
	ConversationID string    `json:"conversationId,omitempty"`
	GroupID        string    `json:"groupId,omitempty"`
	TenantID       string    `json:"tenantId,omitempty"`
	TaskText       string    `json:"taskText,omitempty"`
	CronExpression string    `json:"cronExpression,omitempty"`
	ScheduledAt    time.Time `json:"scheduledAt,omitempty"`
	ExpiresAt      time.Time `json:"expiresAt,omitempty"`
	NextFireAt     time.Time `json:"nextFireAt,omitempty"`
	MaxRuns        int       `json:"maxRuns,omitempty"`
	RunCount       int       `json:"runCount,omitempty"`
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
	document := taskDocument{ID: value.ID, UserID: value.UserID, ChatID: value.ChatID, ChatType: value.ChatType, RootMessageID: value.RootMessageID, ConversationID: value.ConversationID, GroupID: value.GroupID, TenantID: value.TenantID, TaskText: value.TaskText, CronExpression: value.CronExpression, ScheduledAt: value.ScheduledAt, ExpiresAt: value.ExpiresAt, NextFireAt: value.NextFireAt, MaxRuns: value.MaxRuns, RunCount: value.RunCount, Background: value.Background, Status: string(value.Status)}
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
			out = append(out, store.ScheduledTask{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, RootMessageID: document.RootMessageID, ConversationID: document.ConversationID, GroupID: document.GroupID, TenantID: document.TenantID, TaskText: document.TaskText, CronExpression: document.CronExpression, ScheduledAt: document.ScheduledAt, ExpiresAt: document.ExpiresAt, NextFireAt: document.NextFireAt, MaxRuns: document.MaxRuns, RunCount: document.RunCount, Background: document.Background, Status: store.ScheduledTaskStatus(document.Status)})
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

type observedEventDocument struct {
	ID          string    `json:"id"`
	SituationID string    `json:"situationId"`
	Source      string    `json:"source,omitempty"`
	Kind        string    `json:"kind,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	PayloadJSON string    `json:"payloadJson,omitempty"`
	ObservedAt  time.Time `json:"observedAt,omitempty"`
}

func (s *Store) ObservedEvents() store.ObservedEventStore { return observedEventStore{s} }

func (s *Store) saveObservedEvent(ctx context.Context, value store.ObservedEvent) error {
	var old observedEventDocument
	hasOld, err := s.get(ctx, s.record("observed", value.ID), &old)
	if err != nil {
		return err
	}
	document := observedEventDocument{ID: value.ID, SituationID: value.SituationID, Source: value.Source, Kind: value.Kind, Summary: value.Summary, PayloadJSON: value.PayloadJSON, ObservedAt: value.ObservedAt}
	return s.tx(ctx, func(pipe redis.Pipeliner) error {
		if hasOld {
			pipe.SRem(ctx, s.index("observed-situation", old.SituationID), value.ID)
		}
		if err := s.setJSON(ctx, pipe, s.record("observed", value.ID), document); err != nil {
			return err
		}
		pipe.SAdd(ctx, s.all("observed"), value.ID)
		pipe.SAdd(ctx, s.index("observed-situation", value.SituationID), value.ID)
		return nil
	})
}

func (s *Store) findObservedEvents(ctx context.Context, situationID string) ([]store.ObservedEvent, error) {
	ids, err := s.client.SMembers(ctx, s.index("observed-situation", situationID)).Result()
	if err != nil {
		return nil, wrap("list observed events", err)
	}
	sort.Strings(ids)
	out := make([]store.ObservedEvent, 0, len(ids))
	for _, id := range ids {
		var document observedEventDocument
		found, err := s.get(ctx, s.record("observed", id), &document)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, store.ObservedEvent{ID: document.ID, SituationID: document.SituationID, Source: document.Source, Kind: document.Kind, Summary: document.Summary, PayloadJSON: document.PayloadJSON, ObservedAt: document.ObservedAt})
		}
	}
	return out, nil
}

type situationDocument struct {
	ID              string    `json:"id"`
	Source          string    `json:"source,omitempty"`
	CorrelationKey  string    `json:"correlationKey,omitempty"`
	Title           string    `json:"title,omitempty"`
	Status          string    `json:"status"`
	Phase           string    `json:"phase"`
	EvaluateAfter   time.Time `json:"evaluateAfter,omitempty"`
	FirstSeenAt     time.Time `json:"firstSeenAt,omitempty"`
	AwaitingSince   time.Time `json:"awaitingSince,omitempty"`
	LastEventAt     time.Time `json:"lastEventAt,omitempty"`
	LastEvaluatedAt time.Time `json:"lastEvaluatedAt,omitempty"`
	ResolvedAt      time.Time `json:"resolvedAt,omitempty"`
	Generation      int       `json:"generation,omitempty"`
	EventCount      int       `json:"eventCount,omitempty"`
	Decision        string    `json:"decision,omitempty"`
	Severity        string    `json:"severity,omitempty"`
	Confidence      float64   `json:"confidence,omitempty"`
	Assessment      string    `json:"assessment,omitempty"`
	LastError       string    `json:"lastError,omitempty"`
	OwnerUserID     string    `json:"ownerUserId,omitempty"`
	ChatID          string    `json:"chatId,omitempty"`
	ChatType        string    `json:"chatType,omitempty"`
	GroupID         string    `json:"groupId,omitempty"`
	TenantID        string    `json:"tenantId,omitempty"`
}

func (s *Store) Situations() store.SituationStore { return situationStore{s} }

func situationDocumentFromValue(value store.Situation) situationDocument {
	return situationDocument{ID: value.ID, Source: value.Source, CorrelationKey: value.CorrelationKey, Title: value.Title, Status: string(value.Status), Phase: string(value.Phase), EvaluateAfter: value.EvaluateAfter, FirstSeenAt: value.FirstSeenAt, AwaitingSince: value.AwaitingSince, LastEventAt: value.LastEventAt, LastEvaluatedAt: value.LastEvaluatedAt, ResolvedAt: value.ResolvedAt, Generation: value.Generation, EventCount: value.EventCount, Decision: string(value.Decision), Severity: value.Severity, Confidence: value.Confidence, Assessment: value.Assessment, LastError: value.LastError, OwnerUserID: value.OwnerUserID, ChatID: value.ChatID, ChatType: value.ChatType, GroupID: value.GroupID, TenantID: value.TenantID}
}

func situationValue(document situationDocument) store.Situation {
	return store.Situation{ID: document.ID, Source: document.Source, CorrelationKey: document.CorrelationKey, Title: document.Title, Status: store.SituationStatus(document.Status), Phase: store.SituationPhase(document.Phase), EvaluateAfter: document.EvaluateAfter, FirstSeenAt: document.FirstSeenAt, AwaitingSince: document.AwaitingSince, LastEventAt: document.LastEventAt, LastEvaluatedAt: document.LastEvaluatedAt, ResolvedAt: document.ResolvedAt, Generation: document.Generation, EventCount: document.EventCount, Decision: store.SituationDecision(document.Decision), Severity: document.Severity, Confidence: document.Confidence, Assessment: document.Assessment, LastError: document.LastError, OwnerUserID: document.OwnerUserID, ChatID: document.ChatID, ChatType: document.ChatType, GroupID: document.GroupID, TenantID: document.TenantID}
}

func (s *Store) saveSituation(ctx context.Context, value store.Situation) error {
	var old situationDocument
	hasOld, err := s.get(ctx, s.record("situation", value.ID), &old)
	if err != nil {
		return err
	}
	document := situationDocumentFromValue(value)
	return s.tx(ctx, func(pipe redis.Pipeliner) error {
		if hasOld {
			pipe.SRem(ctx, s.index("situation-correlation-status", old.CorrelationKey+"\x00"+old.Status), value.ID)
			pipe.SRem(ctx, s.index("situation-source-correlation-status", old.Source+"\x00"+old.CorrelationKey+"\x00"+old.Status), value.ID)
			pipe.SRem(ctx, s.index("situation-status", old.Status), value.ID)
			pipe.SRem(ctx, s.index("situation-phase", old.Phase), value.ID)
		}
		if err := s.setJSON(ctx, pipe, s.record("situation", value.ID), document); err != nil {
			return err
		}
		pipe.SAdd(ctx, s.all("situation"), value.ID)
		pipe.SAdd(ctx, s.index("situation-correlation-status", value.CorrelationKey+"\x00"+string(value.Status)), value.ID)
		pipe.SAdd(ctx, s.index("situation-source-correlation-status", value.Source+"\x00"+value.CorrelationKey+"\x00"+string(value.Status)), value.ID)
		pipe.SAdd(ctx, s.index("situation-status", string(value.Status)), value.ID)
		pipe.SAdd(ctx, s.index("situation-phase", string(value.Phase)), value.ID)
		return nil
	})
}

func (s *Store) getSituations(ctx context.Context, ids []string) ([]store.Situation, error) {
	sort.Strings(ids)
	out := make([]store.Situation, 0, len(ids))
	for _, id := range ids {
		var document situationDocument
		found, err := s.get(ctx, s.record("situation", id), &document)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, situationValue(document))
		}
	}
	return out, nil
}

func (s *Store) findSituationsByIndex(ctx context.Context, index, value string) ([]store.Situation, error) {
	ids, err := s.client.SMembers(ctx, s.index(index, value)).Result()
	if err != nil {
		return nil, wrap("list situations", err)
	}
	return s.getSituations(ctx, ids)
}

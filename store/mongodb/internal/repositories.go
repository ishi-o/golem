package mongodb

import (
	"context"
	"errors"
	"time"

	"github.com/ishi-o/golem/core/store"
	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
)

type mcpDocument struct {
	ID          string             `bson:"_id"`
	OwnerID     string             `bson:"owner_id"`
	Name        string             `bson:"name"`
	Transport   store.MCPTransport `bson:"transport"`
	URL         string             `bson:"url"`
	Headers     map[string]string  `bson:"headers,omitempty"`
	Title       string             `bson:"title,omitempty"`
	Version     string             `bson:"version,omitempty"`
	Description string             `bson:"description,omitempty"`
	WebsiteURL  string             `bson:"website_url,omitempty"`
	Enabled     bool               `bson:"enabled"`
	SharedWith  []string           `bson:"shared_with,omitempty"`
}

func (s *Store) MCPServerConfigs() store.MCPServerConfigStore { return mcpStore{s} }

func (s *Store) saveMCP(ctx context.Context, value store.MCPServerConfig) error {
	document := mcpDocument{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Transport: value.Transport, URL: value.URL, Headers: value.Headers, Title: value.Title, Version: value.Version, Description: value.Description, WebsiteURL: value.WebsiteURL, Enabled: value.Enabled, SharedWith: value.SharedWith}
	_, err := s.collection("mcp_servers").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save MCP server", err)
}

func (s *Store) findMCPBy(ctx context.Context, filter bson.M) ([]store.MCPServerConfig, error) {
	cursor, err := s.collection("mcp_servers").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("find MCP servers", err)
	}
	defer cursor.Close(ctx)
	var documents []mcpDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode MCP servers", err)
	}
	out := make([]store.MCPServerConfig, 0, len(documents))
	for _, document := range documents {
		out = append(out, store.MCPServerConfig{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Transport: document.Transport, URL: document.URL, Headers: document.Headers, Title: document.Title, Version: document.Version, Description: document.Description, WebsiteURL: document.WebsiteURL, Enabled: document.Enabled, SharedWith: document.SharedWith})
	}
	return out, nil
}

func (s *Store) findOneMCP(ctx context.Context, filter bson.M) (*store.MCPServerConfig, error) {
	var document mcpDocument
	err := s.collection("mcp_servers").FindOne(ctx, filter).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap("find MCP server", err)
	}
	value := store.MCPServerConfig{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Transport: document.Transport, URL: document.URL, Headers: document.Headers, Title: document.Title, Version: document.Version, Description: document.Description, WebsiteURL: document.WebsiteURL, Enabled: document.Enabled, SharedWith: document.SharedWith}
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

func (s *Store) PendingQuestions() store.PendingQuestionStore { return pendingStore{s} }

func (s *Store) savePending(ctx context.Context, value store.PendingQuestion) error {
	document := pendingDocument{ID: value.ID, UserID: value.UserID, ChatID: value.ChatID, ChatType: value.ChatType, ConversationID: value.ConversationID, RootMessageID: value.RootMessageID, CardID: value.CardID, QuestionsJSON: value.QuestionsJSON, Status: string(value.Status), CreatedAt: value.CreatedAt, ExpiresAt: value.ExpiresAt}
	_, err := s.collection("pending_questions").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save pending question", err)
}

func (s *Store) findPending(ctx context.Context, filter bson.M) ([]store.PendingQuestion, error) {
	cursor, err := s.collection("pending_questions").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("find pending questions", err)
	}
	defer cursor.Close(ctx)
	var documents []pendingDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode pending questions", err)
	}
	out := make([]store.PendingQuestion, 0, len(documents))
	for _, document := range documents {
		out = append(out, pendingValue(document))
	}
	return out, nil
}

func pendingValue(document pendingDocument) store.PendingQuestion {
	return store.PendingQuestion{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, ConversationID: document.ConversationID, RootMessageID: document.RootMessageID, CardID: document.CardID, QuestionsJSON: document.QuestionsJSON, Status: store.PendingQuestionStatus(document.Status), CreatedAt: document.CreatedAt, ExpiresAt: document.ExpiresAt}
}

type resourceDocument struct {
	ID            string    `bson:"_id"`
	OwnerID       string    `bson:"owner_id,omitempty"`
	Visibility    string    `bson:"visibility,omitempty"`
	Directory     bool      `bson:"directory"`
	EntryFilename string    `bson:"entry_filename,omitempty"`
	ExpiresAt     time.Time `bson:"expires_at,omitempty"`
}

func (s *Store) PublishedResources() store.PublishedResourceStore { return resourceStore{s} }

func (s *Store) saveResource(ctx context.Context, value store.PublishedResource) error {
	document := resourceDocument{ID: value.ID, OwnerID: value.OwnerID, Visibility: string(value.Visibility), Directory: value.Directory, EntryFilename: value.EntryFilename, ExpiresAt: value.ExpiresAt}
	_, err := s.collection("published_resources").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save published resource", err)
}

func (s *Store) findResource(ctx context.Context, id string) (*store.PublishedResource, error) {
	var document resourceDocument
	err := s.collection("published_resources").FindOne(ctx, bson.M{"_id": id}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap("find published resource", err)
	}
	return &store.PublishedResource{ID: document.ID, OwnerID: document.OwnerID, Visibility: store.Visibility(document.Visibility), Directory: document.Directory, EntryFilename: document.EntryFilename, ExpiresAt: document.ExpiresAt}, nil
}

type taskDocument struct {
	ID             string    `bson:"_id"`
	UserID         string    `bson:"user_id,omitempty"`
	ChatID         string    `bson:"chat_id,omitempty"`
	ChatType       string    `bson:"chat_type,omitempty"`
	RootMessageID  string    `bson:"root_message_id,omitempty"`
	ConversationID string    `bson:"conversation_id,omitempty"`
	GroupID        string    `bson:"group_id,omitempty"`
	TenantID       string    `bson:"tenant_id,omitempty"`
	TaskText       string    `bson:"task_text,omitempty"`
	CronExpression string    `bson:"cron_expression,omitempty"`
	ScheduledAt    time.Time `bson:"scheduled_at,omitempty"`
	ExpiresAt      time.Time `bson:"expires_at,omitempty"`
	NextFireAt     time.Time `bson:"next_fire_at,omitempty"`
	MaxRuns        int       `bson:"max_runs,omitempty"`
	RunCount       int       `bson:"run_count,omitempty"`
	Background     bool      `bson:"background"`
	Status         string    `bson:"status"`
}

func (s *Store) ScheduledTasks() store.ScheduledTaskStore { return taskStore{s} }

func (s *Store) saveTask(ctx context.Context, value store.ScheduledTask) error {
	document := taskDocument{ID: value.ID, UserID: value.UserID, ChatID: value.ChatID, ChatType: value.ChatType, RootMessageID: value.RootMessageID, ConversationID: value.ConversationID, GroupID: value.GroupID, TenantID: value.TenantID, TaskText: value.TaskText, CronExpression: value.CronExpression, ScheduledAt: value.ScheduledAt, ExpiresAt: value.ExpiresAt, NextFireAt: value.NextFireAt, MaxRuns: value.MaxRuns, RunCount: value.RunCount, Background: value.Background, Status: string(value.Status)}
	_, err := s.collection("scheduled_tasks").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save scheduled task", err)
}

func (s *Store) findOneTask(ctx context.Context, id string) (*store.ScheduledTask, error) {
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

func (s *Store) findTasks(ctx context.Context, filter bson.M) ([]store.ScheduledTask, error) {
	cursor, err := s.collection("scheduled_tasks").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	defer cursor.Close(ctx)
	var documents []taskDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode scheduled tasks", err)
	}
	out := make([]store.ScheduledTask, 0, len(documents))
	for _, document := range documents {
		out = append(out, *taskValue(document))
	}
	return out, nil
}

func taskValue(document taskDocument) *store.ScheduledTask {
	return &store.ScheduledTask{ID: document.ID, UserID: document.UserID, ChatID: document.ChatID, ChatType: document.ChatType, RootMessageID: document.RootMessageID, ConversationID: document.ConversationID, GroupID: document.GroupID, TenantID: document.TenantID, TaskText: document.TaskText, CronExpression: document.CronExpression, ScheduledAt: document.ScheduledAt, ExpiresAt: document.ExpiresAt, NextFireAt: document.NextFireAt, MaxRuns: document.MaxRuns, RunCount: document.RunCount, Background: document.Background, Status: store.ScheduledTaskStatus(document.Status)}
}

type credentialDocument struct {
	ID      string `bson:"_id"`
	OwnerID string `bson:"owner_id"`
	Name    string `bson:"name"`
	Value   string `bson:"value,omitempty"`
}

func (s *Store) ShellCredentials() store.ShellCredentialStore { return credentialStore{s} }

func (s *Store) saveCredential(ctx context.Context, value store.ShellCredential) error {
	value.ID = store.ShellCredentialID(value.OwnerID, value.Name)
	document := credentialDocument{ID: value.ID, OwnerID: value.OwnerID, Name: value.Name, Value: value.Value}
	_, err := s.collection("shell_credentials").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save shell credential", err)
}

func (s *Store) findCredentials(ctx context.Context, filter bson.M) ([]store.ShellCredential, error) {
	cursor, err := s.collection("shell_credentials").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, wrap("find shell credentials", err)
	}
	defer cursor.Close(ctx)
	var documents []credentialDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode shell credentials", err)
	}
	out := make([]store.ShellCredential, 0, len(documents))
	for _, document := range documents {
		out = append(out, store.ShellCredential{ID: document.ID, OwnerID: document.OwnerID, Name: document.Name, Value: document.Value})
	}
	return out, nil
}

func (s *Store) ProcessedMessages() store.ProcessedMessageStore { return processedStore{s} }

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

type observedEventDocument struct {
	ID          string    `bson:"_id"`
	SituationID string    `bson:"situation_id"`
	Source      string    `bson:"source,omitempty"`
	Kind        string    `bson:"kind,omitempty"`
	Summary     string    `bson:"summary,omitempty"`
	PayloadJSON string    `bson:"payload_json,omitempty"`
	ObservedAt  time.Time `bson:"observed_at,omitempty"`
}

func (s *Store) ObservedEvents() store.ObservedEventStore { return observedEventStore{s} }

func (s *Store) saveObservedEvent(ctx context.Context, value store.ObservedEvent) error {
	document := observedEventDocument{ID: value.ID, SituationID: value.SituationID, Source: value.Source, Kind: value.Kind, Summary: value.Summary, PayloadJSON: value.PayloadJSON, ObservedAt: value.ObservedAt}
	_, err := s.collection("observed_events").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save observed event", err)
}

func (s *Store) findObservedEvents(ctx context.Context, situationID string) ([]store.ObservedEvent, error) {
	cursor, err := s.collection("observed_events").Find(ctx, bson.M{"situation_id": situationID}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("list observed events", err)
	}
	defer cursor.Close(ctx)
	var documents []observedEventDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode observed events", err)
	}
	out := make([]store.ObservedEvent, 0, len(documents))
	for _, document := range documents {
		out = append(out, store.ObservedEvent{ID: document.ID, SituationID: document.SituationID, Source: document.Source, Kind: document.Kind, Summary: document.Summary, PayloadJSON: document.PayloadJSON, ObservedAt: document.ObservedAt})
	}
	return out, nil
}

type situationDocument struct {
	ID              string    `bson:"_id"`
	Source          string    `bson:"source,omitempty"`
	CorrelationKey  string    `bson:"correlation_key,omitempty"`
	Title           string    `bson:"title,omitempty"`
	Status          string    `bson:"status"`
	Phase           string    `bson:"phase"`
	EvaluateAfter   time.Time `bson:"evaluate_after,omitempty"`
	FirstSeenAt     time.Time `bson:"first_seen_at,omitempty"`
	AwaitingSince   time.Time `bson:"awaiting_since,omitempty"`
	LastEventAt     time.Time `bson:"last_event_at,omitempty"`
	LastEvaluatedAt time.Time `bson:"last_evaluated_at,omitempty"`
	ResolvedAt      time.Time `bson:"resolved_at,omitempty"`
	Generation      int       `bson:"generation,omitempty"`
	EventCount      int       `bson:"event_count,omitempty"`
	Decision        string    `bson:"decision,omitempty"`
	Severity        string    `bson:"severity,omitempty"`
	Confidence      float64   `bson:"confidence,omitempty"`
	Assessment      string    `bson:"assessment,omitempty"`
	LastError       string    `bson:"last_error,omitempty"`
	OwnerUserID     string    `bson:"owner_user_id,omitempty"`
	ChatID          string    `bson:"chat_id,omitempty"`
	ChatType        string    `bson:"chat_type,omitempty"`
	GroupID         string    `bson:"group_id,omitempty"`
	TenantID        string    `bson:"tenant_id,omitempty"`
}

func (s *Store) Situations() store.SituationStore { return situationStore{s} }

func (s *Store) saveSituation(ctx context.Context, value store.Situation) error {
	document := situationDocument{ID: value.ID, Source: value.Source, CorrelationKey: value.CorrelationKey, Title: value.Title, Status: string(value.Status), Phase: string(value.Phase), EvaluateAfter: value.EvaluateAfter, FirstSeenAt: value.FirstSeenAt, AwaitingSince: value.AwaitingSince, LastEventAt: value.LastEventAt, LastEvaluatedAt: value.LastEvaluatedAt, ResolvedAt: value.ResolvedAt, Generation: value.Generation, EventCount: value.EventCount, Decision: string(value.Decision), Severity: value.Severity, Confidence: value.Confidence, Assessment: value.Assessment, LastError: value.LastError, OwnerUserID: value.OwnerUserID, ChatID: value.ChatID, ChatType: value.ChatType, GroupID: value.GroupID, TenantID: value.TenantID}
	_, err := s.collection("situations").ReplaceOne(ctx, bson.M{"_id": value.ID}, document, options.Replace().SetUpsert(true))
	return wrap("save situation", err)
}

func situationValue(document situationDocument) store.Situation {
	return store.Situation{ID: document.ID, Source: document.Source, CorrelationKey: document.CorrelationKey, Title: document.Title, Status: store.SituationStatus(document.Status), Phase: store.SituationPhase(document.Phase), EvaluateAfter: document.EvaluateAfter, FirstSeenAt: document.FirstSeenAt, AwaitingSince: document.AwaitingSince, LastEventAt: document.LastEventAt, LastEvaluatedAt: document.LastEvaluatedAt, ResolvedAt: document.ResolvedAt, Generation: document.Generation, EventCount: document.EventCount, Decision: store.SituationDecision(document.Decision), Severity: document.Severity, Confidence: document.Confidence, Assessment: document.Assessment, LastError: document.LastError, OwnerUserID: document.OwnerUserID, ChatID: document.ChatID, ChatType: document.ChatType, GroupID: document.GroupID, TenantID: document.TenantID}
}

func (s *Store) getSituation(ctx context.Context, id string) (*store.Situation, error) {
	var document situationDocument
	err := s.collection("situations").FindOne(ctx, bson.M{"_id": id}).Decode(&document)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, wrap("find situation", err)
	}
	value := situationValue(document)
	return &value, nil
}

func (s *Store) findSituations(ctx context.Context, filter bson.M) ([]store.Situation, error) {
	cursor, err := s.collection("situations").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, wrap("list situations", err)
	}
	defer cursor.Close(ctx)
	var documents []situationDocument
	if err := cursor.All(ctx, &documents); err != nil {
		return nil, wrap("decode situations", err)
	}
	out := make([]store.Situation, 0, len(documents))
	for _, document := range documents {
		out = append(out, situationValue(document))
	}
	return out, nil
}

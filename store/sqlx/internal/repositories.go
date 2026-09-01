package sqlx

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/ishi-o/golem/core/store"
)

type mcpRow struct {
	ID             string         `db:"id"`
	OwnerID        string         `db:"owner_id"`
	Name           string         `db:"name"`
	Transport      string         `db:"transport"`
	URL            string         `db:"url"`
	HeadersJSON    sql.NullString `db:"headers_json"`
	Title          sql.NullString `db:"title"`
	Version        sql.NullString `db:"version"`
	Description    sql.NullString `db:"description"`
	WebsiteURL     sql.NullString `db:"website_url"`
	Enabled        int            `db:"enabled"`
	SharedWithJSON sql.NullString `db:"shared_with_json"`
}

func (s *Store) saveMCP(ctx context.Context, config store.MCPServerConfig) error {
	headers, err := marshal(config.Headers)
	if err != nil {
		return fmt.Errorf("mcp headers: %w", err)
	}
	sharedWith, err := marshal(config.SharedWith)
	if err != nil {
		return fmt.Errorf("mcp sharedWith: %w", err)
	}
	query := s.upsert(s.table("mcp_servers"),
		[]string{"id", "owner_id", "name", "transport", "url", "headers_json", "title", "version", "description", "website_url", "enabled", "shared_with_json"},
		[]string{"id", "owner_id", "name", "transport", "url", "headers_json", "title", "version", "description", "website_url", "enabled", "shared_with_json"})
	_, err = s.db.ExecContext(ctx, s.rebind(query), config.ID, config.OwnerID, config.Name, string(config.Transport), config.URL, headers, config.Title, config.Version, config.Description, config.WebsiteURL, boolInt(config.Enabled), sharedWith)
	return wrap("save MCP server", err)
}

func (s *Store) MCPServerConfigs() store.MCPServerConfigStore { return mcpStore{s} }

func (s *Store) listMCPByOwner(ctx context.Context, ownerID string) ([]store.MCPServerConfig, error) {
	var rows []mcpRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, transport, url, headers_json, title, version, description, website_url, enabled, shared_with_json FROM %s WHERE owner_id = ? ORDER BY id`, s.table("mcp_servers"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), ownerID); err != nil {
		return nil, wrap("find MCP servers by owner", err)
	}
	return mapMCP(rows)
}

func (s *Store) getMCPByOwnerAndName(ctx context.Context, ownerID, name string) (*store.MCPServerConfig, error) {
	var row mcpRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, transport, url, headers_json, title, version, description, website_url, enabled, shared_with_json FROM %s WHERE owner_id = ? AND name = ?`, s.table("mcp_servers"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), ownerID, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find MCP server by owner and name", err)
	}
	configs, err := mapMCP([]mcpRow{row})
	if err != nil || len(configs) == 0 {
		return nil, err
	}
	return &configs[0], nil
}

func (s *Store) existsMCPByOwnerAndName(ctx context.Context, ownerID, name string) (bool, error) {
	var exists int
	query := fmt.Sprintf(`SELECT 1 FROM %s WHERE owner_id = ? AND name = ?`, s.table("mcp_servers"))
	err := s.db.GetContext(ctx, &exists, s.rebind(query), ownerID, name)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, wrap("check MCP server", err)
}

func (s *Store) deleteMCPByOwnerAndName(ctx context.Context, ownerID, name string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE owner_id = ? AND name = ?`, s.table("mcp_servers"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), ownerID, name)
	return wrap("delete MCP server", err)
}

func (s *Store) listMCPSharedWith(ctx context.Context, identifiers []string) ([]store.MCPServerConfig, error) {
	configs, err := s.allMCP(ctx)
	if err != nil {
		return nil, err
	}
	set := make(map[string]struct{}, len(identifiers))
	for _, identifier := range identifiers {
		set[identifier] = struct{}{}
	}
	out := configs[:0]
	for _, config := range configs {
		for _, sharedWith := range config.SharedWith {
			if _, ok := set[sharedWith]; ok {
				out = append(out, config)
				break
			}
		}
	}
	return out, nil
}

func (s *Store) listMCPAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]store.MCPServerConfig, error) {
	owned, err := s.listMCPByOwner(ctx, ownerID)
	if err != nil {
		return nil, err
	}
	shared, err := s.listMCPSharedWith(ctx, identifiers)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]struct{}, len(owned)+len(shared))
	out := make([]store.MCPServerConfig, 0, len(owned)+len(shared))
	for _, config := range append(owned, shared...) {
		if _, ok := seen[config.ID]; ok {
			continue
		}
		seen[config.ID] = struct{}{}
		out = append(out, config)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *Store) allMCP(ctx context.Context) ([]store.MCPServerConfig, error) {
	var rows []mcpRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, transport, url, headers_json, title, version, description, website_url, enabled, shared_with_json FROM %s ORDER BY id`, s.table("mcp_servers"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query)); err != nil {
		return nil, wrap("list MCP servers", err)
	}
	return mapMCP(rows)
}

func mapMCP(rows []mcpRow) ([]store.MCPServerConfig, error) {
	out := make([]store.MCPServerConfig, 0, len(rows))
	for _, row := range rows {
		var headers map[string]string
		var sharedWith []string
		if err := unmarshal(row.HeadersJSON, &headers); err != nil {
			return nil, fmt.Errorf("decode MCP headers: %w", err)
		}
		if err := unmarshal(row.SharedWithJSON, &sharedWith); err != nil {
			return nil, fmt.Errorf("decode MCP sharedWith: %w", err)
		}
		out = append(out, store.MCPServerConfig{ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, Transport: store.MCPTransport(row.Transport), URL: row.URL, Headers: headers, Title: row.Title.String, Version: row.Version.String, Description: row.Description.String, WebsiteURL: row.WebsiteURL.String, Enabled: row.Enabled != 0, SharedWith: sharedWith})
	}
	return out, nil
}

type pendingRow struct {
	ID             string         `db:"id"`
	UserID         sql.NullString `db:"user_id"`
	ChatID         sql.NullString `db:"chat_id"`
	ChatType       sql.NullString `db:"chat_type"`
	ConversationID sql.NullString `db:"conversation_id"`
	RootMessageID  sql.NullString `db:"root_message_id"`
	CardID         sql.NullString `db:"card_id"`
	QuestionsJSON  sql.NullString `db:"questions_json"`
	Status         string         `db:"status"`
	CreatedAt      sql.NullString `db:"created_at"`
	ExpiresAt      sql.NullString `db:"expires_at"`
}

func (s *Store) PendingQuestions() store.PendingQuestionStore { return pendingStore{s} }

func (s *Store) savePendingQuestion(ctx context.Context, question store.PendingQuestion) error {
	query := s.upsert(s.table("pending_questions"), []string{"id", "user_id", "chat_id", "chat_type", "conversation_id", "root_message_id", "card_id", "questions_json", "status", "created_at", "expires_at"}, []string{"id", "user_id", "chat_id", "chat_type", "conversation_id", "root_message_id", "card_id", "questions_json", "status", "created_at", "expires_at"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), question.ID, question.UserID, question.ChatID, question.ChatType, question.ConversationID, question.RootMessageID, question.CardID, question.QuestionsJSON, string(question.Status), timeValue(question.CreatedAt), timeValue(question.ExpiresAt))
	return wrap("save pending question", err)
}

func (s *Store) getPending(ctx context.Context, id string) (*store.PendingQuestion, error) {
	var row pendingRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, conversation_id, root_message_id, card_id, questions_json, status, created_at, expires_at FROM %s WHERE id = ?`, s.table("pending_questions"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find pending question", err)
	}
	question, err := pendingFromRow(row)
	if err != nil {
		return nil, err
	}
	return &question, nil
}

func (s *Store) listPendingByConversationAndStatus(ctx context.Context, conversationID string, status store.PendingQuestionStatus) ([]store.PendingQuestion, error) {
	var rows []pendingRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, conversation_id, root_message_id, card_id, questions_json, status, created_at, expires_at FROM %s WHERE conversation_id = ? AND status = ? ORDER BY id`, s.table("pending_questions"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), conversationID, string(status)); err != nil {
		return nil, wrap("find pending questions", err)
	}
	out := make([]store.PendingQuestion, 0, len(rows))
	for _, row := range rows {
		question, err := pendingFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, question)
	}
	return out, nil
}

func (s *Store) setPendingStatus(ctx context.Context, id string, status store.PendingQuestionStatus) error {
	query := fmt.Sprintf(`UPDATE %s SET status = ? WHERE id = ?`, s.table("pending_questions"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), string(status), id)
	return wrap("update pending question status", err)
}

func pendingFromRow(row pendingRow) (store.PendingQuestion, error) {
	createdAt, err := parseTime(row.CreatedAt)
	if err != nil {
		return store.PendingQuestion{}, fmt.Errorf("decode pending question createdAt: %w", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return store.PendingQuestion{}, fmt.Errorf("decode pending question expiresAt: %w", err)
	}
	return store.PendingQuestion{ID: row.ID, UserID: row.UserID.String, ChatID: row.ChatID.String, ChatType: row.ChatType.String, ConversationID: row.ConversationID.String, RootMessageID: row.RootMessageID.String, CardID: row.CardID.String, QuestionsJSON: row.QuestionsJSON.String, Status: store.PendingQuestionStatus(row.Status), CreatedAt: createdAt, ExpiresAt: expiresAt}, nil
}

type resourceRow struct {
	ID            string         `db:"id"`
	OwnerID       sql.NullString `db:"owner_id"`
	Visibility    sql.NullString `db:"visibility"`
	Directory     int            `db:"directory"`
	EntryFilename sql.NullString `db:"entry_filename"`
	ExpiresAt     sql.NullString `db:"expires_at"`
}

func (s *Store) PublishedResources() store.PublishedResourceStore { return resourceStore{s} }

func (s *Store) savePublishedResource(ctx context.Context, resource store.PublishedResource) error {
	query := s.upsert(s.table("published_resources"), []string{"id", "owner_id", "visibility", "directory", "entry_filename", "expires_at"}, []string{"id", "owner_id", "visibility", "directory", "entry_filename", "expires_at"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), resource.ID, resource.OwnerID, string(resource.Visibility), boolInt(resource.Directory), resource.EntryFilename, timeValue(resource.ExpiresAt))
	return wrap("save published resource", err)
}

func (s *Store) findPublishedResource(ctx context.Context, id string) (*store.PublishedResource, error) {
	var row resourceRow
	query := fmt.Sprintf(`SELECT id, owner_id, visibility, directory, entry_filename, expires_at FROM %s WHERE id = ?`, s.table("published_resources"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find published resource", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return nil, fmt.Errorf("decode published resource expiry: %w", err)
	}
	return &store.PublishedResource{ID: row.ID, OwnerID: row.OwnerID.String, Visibility: store.Visibility(row.Visibility.String), Directory: row.Directory != 0, EntryFilename: row.EntryFilename.String, ExpiresAt: expiresAt}, nil
}

func (s *Store) deletePublishedResource(ctx context.Context, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table("published_resources"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), id)
	return wrap("delete published resource", err)
}

type taskRow struct {
	ID             string         `db:"id"`
	UserID         sql.NullString `db:"user_id"`
	ChatID         sql.NullString `db:"chat_id"`
	ChatType       sql.NullString `db:"chat_type"`
	RootMessageID  sql.NullString `db:"root_message_id"`
	ConversationID sql.NullString `db:"conversation_id"`
	GroupID        sql.NullString `db:"group_id"`
	TenantID       sql.NullString `db:"tenant_id"`
	TaskText       sql.NullString `db:"task_text"`
	CronExpression sql.NullString `db:"cron_expression"`
	ScheduledAt    sql.NullString `db:"scheduled_at"`
	ExpiresAt      sql.NullString `db:"expires_at"`
	NextFireAt     sql.NullString `db:"next_fire_at"`
	MaxRuns        sql.NullInt64  `db:"max_runs"`
	RunCount       sql.NullInt64  `db:"run_count"`
	Background     int            `db:"background"`
	Status         string         `db:"status"`
}

func (s *Store) ScheduledTasks() store.ScheduledTaskStore { return taskStore{s} }

func (s *Store) saveScheduledTask(ctx context.Context, task store.ScheduledTask) error {
	columns := []string{"id", "user_id", "chat_id", "chat_type", "root_message_id", "conversation_id", "group_id", "tenant_id", "task_text", "cron_expression", "scheduled_at", "expires_at", "next_fire_at", "max_runs", "run_count", "background", "status"}
	query := s.upsert(s.table("scheduled_tasks"), columns, columns)
	_, err := s.db.ExecContext(ctx, s.rebind(query), task.ID, task.UserID, task.ChatID, task.ChatType, task.RootMessageID, task.ConversationID, task.GroupID, task.TenantID, task.TaskText, task.CronExpression, timeValue(task.ScheduledAt), timeValue(task.ExpiresAt), timeValue(task.NextFireAt), task.MaxRuns, task.RunCount, boolInt(task.Background), string(task.Status))
	return wrap("save scheduled task", err)
}

func (s *Store) findScheduledTask(ctx context.Context, id string) (*store.ScheduledTask, error) {
	var row taskRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, root_message_id, conversation_id, group_id, tenant_id, task_text, cron_expression, scheduled_at, expires_at, next_fire_at, max_runs, run_count, background, status FROM %s WHERE id = ?`, s.table("scheduled_tasks"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find scheduled task", err)
	}
	task, err := taskFromRow(row)
	if err != nil {
		return nil, err
	}
	return &task, nil
}

func (s *Store) listTasksByStatus(ctx context.Context, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	return s.findTasks(ctx, `status = ?`, string(status))
}

func (s *Store) listTasksByUserAndStatus(ctx context.Context, userID string, status store.ScheduledTaskStatus) ([]store.ScheduledTask, error) {
	return s.findTasks(ctx, `user_id = ? AND status = ?`, userID, string(status))
}

func (s *Store) findTasks(ctx context.Context, where string, args ...any) ([]store.ScheduledTask, error) {
	var rows []taskRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, root_message_id, conversation_id, group_id, tenant_id, task_text, cron_expression, scheduled_at, expires_at, next_fire_at, max_runs, run_count, background, status FROM %s WHERE %s ORDER BY id`, s.table("scheduled_tasks"), where)
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), args...); err != nil {
		return nil, wrap("find scheduled tasks", err)
	}
	out := make([]store.ScheduledTask, 0, len(rows))
	for _, row := range rows {
		task, err := taskFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, task)
	}
	return out, nil
}

func (s *Store) updateTaskStatus(ctx context.Context, id string, status store.ScheduledTaskStatus) error {
	query := fmt.Sprintf(`UPDATE %s SET status = ? WHERE id = ?`, s.table("scheduled_tasks"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), string(status), id)
	return wrap("update scheduled task status", err)
}

func taskFromRow(row taskRow) (store.ScheduledTask, error) {
	scheduledAt, err := parseTime(row.ScheduledAt)
	if err != nil {
		return store.ScheduledTask{}, fmt.Errorf("decode scheduled task time: %w", err)
	}
	expiresAt, err := parseTime(row.ExpiresAt)
	if err != nil {
		return store.ScheduledTask{}, fmt.Errorf("decode scheduled task expiry: %w", err)
	}
	nextFireAt, err := parseTime(row.NextFireAt)
	if err != nil {
		return store.ScheduledTask{}, fmt.Errorf("decode scheduled task next fire time: %w", err)
	}
	return store.ScheduledTask{ID: row.ID, UserID: row.UserID.String, ChatID: row.ChatID.String, ChatType: row.ChatType.String, RootMessageID: row.RootMessageID.String, ConversationID: row.ConversationID.String, GroupID: row.GroupID.String, TenantID: row.TenantID.String, TaskText: row.TaskText.String, CronExpression: row.CronExpression.String, ScheduledAt: scheduledAt, ExpiresAt: expiresAt, NextFireAt: nextFireAt, MaxRuns: int(row.MaxRuns.Int64), RunCount: int(row.RunCount.Int64), Background: row.Background != 0, Status: store.ScheduledTaskStatus(row.Status)}, nil
}

type credentialRow struct {
	ID      string         `db:"id"`
	OwnerID string         `db:"owner_id"`
	Name    string         `db:"name"`
	Value   sql.NullString `db:"value"`
}

func (s *Store) ShellCredentials() store.ShellCredentialStore { return credentialStore{s} }

func (s *Store) saveShellCredential(ctx context.Context, credential store.ShellCredential) error {
	credential.ID = store.ShellCredentialID(credential.OwnerID, credential.Name)
	query := s.upsert(s.table("shell_credentials"), []string{"id", "owner_id", "name", "value"}, []string{"id", "owner_id", "name", "value"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), credential.ID, credential.OwnerID, credential.Name, credential.Value)
	return wrap("save shell credential", err)
}

func (s *Store) listCredentialsByOwner(ctx context.Context, ownerID string) ([]store.ShellCredential, error) {
	var rows []credentialRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, value FROM %s WHERE owner_id = ? ORDER BY name`, s.table("shell_credentials"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), ownerID); err != nil {
		return nil, wrap("find shell credentials", err)
	}
	return mapCredentials(rows), nil
}

func (s *Store) getCredentialByOwnerAndName(ctx context.Context, ownerID, name string) (*store.ShellCredential, error) {
	var row credentialRow
	query := fmt.Sprintf(`SELECT id, owner_id, name, value FROM %s WHERE owner_id = ? AND name = ?`, s.table("shell_credentials"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), ownerID, name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find shell credential", err)
	}
	credentials := mapCredentials([]credentialRow{row})
	return &credentials[0], nil
}

func (s *Store) deleteCredentialByOwnerAndName(ctx context.Context, ownerID, name string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE owner_id = ? AND name = ?`, s.table("shell_credentials"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), ownerID, name)
	return wrap("delete shell credential", err)
}

func mapCredentials(rows []credentialRow) []store.ShellCredential {
	out := make([]store.ShellCredential, 0, len(rows))
	for _, row := range rows {
		out = append(out, store.ShellCredential{ID: row.ID, OwnerID: row.OwnerID, Name: row.Name, Value: row.Value.String})
	}
	return out
}

func (s *Store) ProcessedMessages() store.ProcessedMessageStore { return processedStore{s} }

func (s *Store) claim(ctx context.Context, id string) (bool, error) {
	query := fmt.Sprintf(`INSERT INTO %s (id, created_at) VALUES (?, ?)`, s.table("processed_messages"))
	switch s.dialect {
	case DialectMySQL:
		query += ` ON DUPLICATE KEY UPDATE id = id`
	default:
		query += ` ON CONFLICT (id) DO NOTHING`
	}
	result, err := s.db.ExecContext(ctx, s.rebind(query), id, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return false, wrap("claim processed message", err)
	}
	count, err := result.RowsAffected()
	return count == 1, wrap("inspect processed message claim", err)
}

func (s *Store) release(ctx context.Context, id string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE id = ?`, s.table("processed_messages"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), id)
	return wrap("release processed message", err)
}

type observedEventRow struct {
	ID          string         `db:"id"`
	SituationID string         `db:"situation_id"`
	Source      sql.NullString `db:"source"`
	Kind        sql.NullString `db:"kind"`
	Summary     sql.NullString `db:"summary"`
	PayloadJSON sql.NullString `db:"payload_json"`
	ObservedAt  sql.NullString `db:"observed_at"`
}

func (s *Store) ObservedEvents() store.ObservedEventStore { return observedEventStore{s} }

func (s *Store) saveObservedEvent(ctx context.Context, event store.ObservedEvent) error {
	query := s.upsert(s.table("observed_events"),
		[]string{"id", "situation_id", "source", "kind", "summary", "payload_json", "observed_at"},
		[]string{"id", "situation_id", "source", "kind", "summary", "payload_json", "observed_at"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), event.ID, event.SituationID, event.Source, event.Kind, event.Summary, event.PayloadJSON, timeValue(event.ObservedAt))
	return wrap("save observed event", err)
}

func (s *Store) listObservedEvents(ctx context.Context, situationID string) ([]store.ObservedEvent, error) {
	var rows []observedEventRow
	query := fmt.Sprintf(`SELECT id, situation_id, source, kind, summary, payload_json, observed_at FROM %s WHERE situation_id = ? ORDER BY id`, s.table("observed_events"))
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), situationID); err != nil {
		return nil, wrap("list observed events", err)
	}
	out := make([]store.ObservedEvent, 0, len(rows))
	for _, row := range rows {
		observedAt, err := parseTime(row.ObservedAt)
		if err != nil {
			return nil, fmt.Errorf("decode observed event time: %w", err)
		}
		out = append(out, store.ObservedEvent{ID: row.ID, SituationID: row.SituationID, Source: row.Source.String, Kind: row.Kind.String, Summary: row.Summary.String, PayloadJSON: row.PayloadJSON.String, ObservedAt: observedAt})
	}
	return out, nil
}

type situationRow struct {
	ID              string          `db:"id"`
	Source          sql.NullString  `db:"source"`
	CorrelationKey  sql.NullString  `db:"correlation_key"`
	Title           sql.NullString  `db:"title"`
	Status          string          `db:"status"`
	Phase           string          `db:"phase"`
	EvaluateAfter   sql.NullString  `db:"evaluate_after"`
	FirstSeenAt     sql.NullString  `db:"first_seen_at"`
	AwaitingSince   sql.NullString  `db:"awaiting_since"`
	LastEventAt     sql.NullString  `db:"last_event_at"`
	LastEvaluatedAt sql.NullString  `db:"last_evaluated_at"`
	ResolvedAt      sql.NullString  `db:"resolved_at"`
	Generation      sql.NullInt64   `db:"generation"`
	EventCount      sql.NullInt64   `db:"event_count"`
	Decision        sql.NullString  `db:"decision"`
	Severity        sql.NullString  `db:"severity"`
	Confidence      sql.NullFloat64 `db:"confidence"`
	Assessment      sql.NullString  `db:"assessment"`
	LastError       sql.NullString  `db:"last_error"`
	OwnerUserID     sql.NullString  `db:"owner_user_id"`
	ChatID          sql.NullString  `db:"chat_id"`
	ChatType        sql.NullString  `db:"chat_type"`
	GroupID         sql.NullString  `db:"group_id"`
	TenantID        sql.NullString  `db:"tenant_id"`
}

func (s *Store) Situations() store.SituationStore { return situationStore{s} }

const situationColumns = `id, source, correlation_key, title, status, phase, evaluate_after, first_seen_at, awaiting_since, last_event_at, last_evaluated_at, resolved_at, generation, event_count, decision, severity, confidence, assessment, last_error, owner_user_id, chat_id, chat_type, group_id, tenant_id`

func (s *Store) saveSituation(ctx context.Context, situation store.Situation) error {
	columns := []string{"id", "source", "correlation_key", "title", "status", "phase", "evaluate_after", "first_seen_at", "awaiting_since", "last_event_at", "last_evaluated_at", "resolved_at", "generation", "event_count", "decision", "severity", "confidence", "assessment", "last_error", "owner_user_id", "chat_id", "chat_type", "group_id", "tenant_id"}
	query := s.upsert(s.table("situations"), columns, columns)
	_, err := s.db.ExecContext(ctx, s.rebind(query), situation.ID, situation.Source, situation.CorrelationKey, situation.Title, string(situation.Status), string(situation.Phase), timeValue(situation.EvaluateAfter), timeValue(situation.FirstSeenAt), timeValue(situation.AwaitingSince), timeValue(situation.LastEventAt), timeValue(situation.LastEvaluatedAt), timeValue(situation.ResolvedAt), situation.Generation, situation.EventCount, string(situation.Decision), situation.Severity, situation.Confidence, situation.Assessment, situation.LastError, situation.OwnerUserID, situation.ChatID, situation.ChatType, situation.GroupID, situation.TenantID)
	return wrap("save situation", err)
}

func (s *Store) getSituation(ctx context.Context, id string) (*store.Situation, error) {
	var row situationRow
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE id = ?`, situationColumns, s.table("situations"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, wrap("find situation", err)
	}
	value, err := situationFromRow(row)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func (s *Store) listSituations(ctx context.Context, where string, args ...any) ([]store.Situation, error) {
	var rows []situationRow
	query := fmt.Sprintf(`SELECT %s FROM %s WHERE %s ORDER BY id`, situationColumns, s.table("situations"), where)
	if err := s.db.SelectContext(ctx, &rows, s.rebind(query), args...); err != nil {
		return nil, wrap("list situations", err)
	}
	out := make([]store.Situation, 0, len(rows))
	for _, row := range rows {
		value, err := situationFromRow(row)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func situationFromRow(row situationRow) (store.Situation, error) {
	parse := func(value sql.NullString) (time.Time, error) { return parseTime(value) }
	evaluateAfter, err := parse(row.EvaluateAfter)
	if err != nil {
		return store.Situation{}, fmt.Errorf("decode situation evaluateAfter: %w", err)
	}
	firstSeenAt, err := parse(row.FirstSeenAt)
	if err != nil {
		return store.Situation{}, fmt.Errorf("decode situation firstSeenAt: %w", err)
	}
	awaitingSince, err := parse(row.AwaitingSince)
	if err != nil {
		return store.Situation{}, fmt.Errorf("decode situation awaitingSince: %w", err)
	}
	lastEventAt, err := parse(row.LastEventAt)
	if err != nil {
		return store.Situation{}, fmt.Errorf("decode situation lastEventAt: %w", err)
	}
	lastEvaluatedAt, err := parse(row.LastEvaluatedAt)
	if err != nil {
		return store.Situation{}, fmt.Errorf("decode situation lastEvaluatedAt: %w", err)
	}
	resolvedAt, err := parse(row.ResolvedAt)
	if err != nil {
		return store.Situation{}, fmt.Errorf("decode situation resolvedAt: %w", err)
	}
	return store.Situation{ID: row.ID, Source: row.Source.String, CorrelationKey: row.CorrelationKey.String, Title: row.Title.String, Status: store.SituationStatus(row.Status), Phase: store.SituationPhase(row.Phase), EvaluateAfter: evaluateAfter, FirstSeenAt: firstSeenAt, AwaitingSince: awaitingSince, LastEventAt: lastEventAt, LastEvaluatedAt: lastEvaluatedAt, ResolvedAt: resolvedAt, Generation: int(row.Generation.Int64), EventCount: int(row.EventCount.Int64), Decision: store.SituationDecision(row.Decision.String), Severity: row.Severity.String, Confidence: row.Confidence.Float64, Assessment: row.Assessment.String, LastError: row.LastError.String, OwnerUserID: row.OwnerUserID.String, ChatID: row.ChatID.String, ChatType: row.ChatType.String, GroupID: row.GroupID.String, TenantID: row.TenantID.String}, nil
}

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
	TaskText       sql.NullString `db:"task_text"`
	CronExpression sql.NullString `db:"cron_expression"`
	ScheduledAt    sql.NullString `db:"scheduled_at"`
	ExpiresAt      sql.NullString `db:"expires_at"`
	Background     int            `db:"background"`
	Status         string         `db:"status"`
}

func (s *Store) ScheduledTasks() store.ScheduledTaskStore { return taskStore{s} }

func (s *Store) saveScheduledTask(ctx context.Context, task store.ScheduledTask) error {
	query := s.upsert(s.table("scheduled_tasks"), []string{"id", "user_id", "chat_id", "chat_type", "root_message_id", "task_text", "cron_expression", "scheduled_at", "expires_at", "background", "status"}, []string{"id", "user_id", "chat_id", "chat_type", "root_message_id", "task_text", "cron_expression", "scheduled_at", "expires_at", "background", "status"})
	_, err := s.db.ExecContext(ctx, s.rebind(query), task.ID, task.UserID, task.ChatID, task.ChatType, task.RootMessageID, task.TaskText, task.CronExpression, timeValue(task.ScheduledAt), timeValue(task.ExpiresAt), boolInt(task.Background), string(task.Status))
	return wrap("save scheduled task", err)
}

func (s *Store) findScheduledTask(ctx context.Context, id string) (*store.ScheduledTask, error) {
	var row taskRow
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, root_message_id, task_text, cron_expression, scheduled_at, expires_at, background, status FROM %s WHERE id = ?`, s.table("scheduled_tasks"))
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
	query := fmt.Sprintf(`SELECT id, user_id, chat_id, chat_type, root_message_id, task_text, cron_expression, scheduled_at, expires_at, background, status FROM %s WHERE %s ORDER BY id`, s.table("scheduled_tasks"), where)
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
	return store.ScheduledTask{ID: row.ID, UserID: row.UserID.String, ChatID: row.ChatID.String, ChatType: row.ChatType.String, RootMessageID: row.RootMessageID.String, TaskText: row.TaskText.String, CronExpression: row.CronExpression.String, ScheduledAt: scheduledAt, ExpiresAt: expiresAt, Background: row.Background != 0, Status: store.ScheduledTaskStatus(row.Status)}, nil
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

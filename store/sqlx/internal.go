package sqlxstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
)

func (s *Store) rebind(query string) string { return s.db.Rebind(query) }

func (s *Store) upsert(table string, columns, updates []string) string {
	placeholders := strings.TrimRight(strings.Repeat("?,", len(columns)), ",")
	query := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", table, strings.Join(columns, ", "), placeholders)
	switch s.dialect {
	case DialectMySQL:
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			assignments = append(assignments, column+" = VALUES("+column+")")
		}
		return query + " ON DUPLICATE KEY UPDATE " + strings.Join(assignments, ", ")
	case DialectGeneric:
		// Generic SQL has no portable upsert. The caller should use a
		// dialect for stores that need concurrent updates; this clause is
		// kept useful for engines that accept SQL-standard MERGE-like
		// aliases through ON CONFLICT.
		fallthrough
	default:
		assignments := make([]string, 0, len(updates))
		for _, column := range updates {
			if column == columns[0] {
				continue
			}
			assignments = append(assignments, column+" = excluded."+column)
		}
		return query + " ON CONFLICT (" + columns[0] + ") DO UPDATE SET " + strings.Join(assignments, ", ")
	}
}

func marshal(value any) (string, error) {
	if value == nil {
		return "", nil
	}
	data, err := json.Marshal(value)
	return string(data), err
}

func unmarshal(value sql.NullString, target any) error {
	if !value.Valid || value.String == "" || value.String == "null" {
		return nil
	}
	return json.Unmarshal([]byte(value.String), target)
}

func timeValue(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func parseTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value.String)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func wrap(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlx store: %s: %w", operation, err)
}

var (
	_ dao.Backend           = (*Store)(nil)
	_ chatmemory.Repository = (*Store)(nil)
)

// The contracts intentionally reuse method names such as Save and FindByID.
// Small repository views keep those names readable without forcing one
// concrete Store to invent backend-specific suffixes in its public API.
type mcpRepo struct{ store *Store }

func (r mcpRepo) Save(ctx context.Context, value dao.McpServerConfig) error {
	return r.store.Save(ctx, value)
}
func (r mcpRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.McpServerConfig, error) {
	return r.store.FindByOwnerID(ctx, ownerID)
}
func (r mcpRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.McpServerConfig, error) {
	return r.store.FindByOwnerIDAndName(ctx, ownerID, name)
}
func (r mcpRepo) ExistsByOwnerIDAndName(ctx context.Context, ownerID, name string) (bool, error) {
	return r.store.ExistsByOwnerIDAndName(ctx, ownerID, name)
}
func (r mcpRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	return r.store.DeleteByOwnerIDAndName(ctx, ownerID, name)
}
func (r mcpRepo) FindBySharedWithIn(ctx context.Context, identifiers []string) ([]dao.McpServerConfig, error) {
	return r.store.FindBySharedWithIn(ctx, identifiers)
}
func (r mcpRepo) FindAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]dao.McpServerConfig, error) {
	return r.store.FindAccessibleTo(ctx, ownerID, identifiers)
}

type pendingRepo struct{ store *Store }

func (r pendingRepo) Save(ctx context.Context, value dao.PendingQuestion) error {
	return r.store.SavePendingQuestion(ctx, value)
}
func (r pendingRepo) FindByID(ctx context.Context, id string) (*dao.PendingQuestion, error) {
	return r.store.FindByID(ctx, id)
}
func (r pendingRepo) FindByConversationIDAndStatus(ctx context.Context, conversationID string, status dao.PendingQuestionStatus) ([]dao.PendingQuestion, error) {
	return r.store.FindByConversationIDAndStatus(ctx, conversationID, status)
}
func (r pendingRepo) UpdateStatus(ctx context.Context, id string, status dao.PendingQuestionStatus) error {
	return r.store.UpdateStatus(ctx, id, status)
}

type resourceRepo struct{ store *Store }

func (r resourceRepo) Save(ctx context.Context, value dao.PublishedResource) error {
	return r.store.SavePublishedResource(ctx, value)
}
func (r resourceRepo) FindByID(ctx context.Context, id string) (*dao.PublishedResource, error) {
	return r.store.FindPublishedResource(ctx, id)
}
func (r resourceRepo) DeleteByID(ctx context.Context, id string) error {
	return r.store.DeletePublishedResource(ctx, id)
}

type taskRepo struct{ store *Store }

func (r taskRepo) Save(ctx context.Context, value dao.ScheduledTask) error {
	return r.store.SaveScheduledTask(ctx, value)
}
func (r taskRepo) FindByID(ctx context.Context, id string) (*dao.ScheduledTask, error) {
	return r.store.FindScheduledTaskByID(ctx, id)
}
func (r taskRepo) FindByStatus(ctx context.Context, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return r.store.FindByStatus(ctx, status)
}
func (r taskRepo) FindByUserIDAndStatus(ctx context.Context, userID string, status dao.ScheduledTaskStatus) ([]dao.ScheduledTask, error) {
	return r.store.FindByUserIDAndStatus(ctx, userID, status)
}
func (r taskRepo) UpdateStatus(ctx context.Context, id string, status dao.ScheduledTaskStatus) error {
	return r.store.UpdateTaskStatus(ctx, id, status)
}

type credentialRepo struct{ store *Store }

func (r credentialRepo) Save(ctx context.Context, value dao.ShellCredential) error {
	return r.store.SaveShellCredential(ctx, value)
}
func (r credentialRepo) FindByOwnerID(ctx context.Context, ownerID string) ([]dao.ShellCredential, error) {
	return r.store.FindByOwnerIDCredentials(ctx, ownerID)
}
func (r credentialRepo) FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*dao.ShellCredential, error) {
	return r.store.FindByOwnerIDAndNameCredential(ctx, ownerID, name)
}
func (r credentialRepo) DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error {
	return r.store.DeleteByOwnerIDAndNameCredential(ctx, ownerID, name)
}

type processedRepo struct{ store *Store }

func (r processedRepo) Claim(ctx context.Context, id string) (bool, error) {
	return r.store.Claim(ctx, id)
}
func (r processedRepo) Release(ctx context.Context, id string) error {
	return r.store.Release(ctx, id)
}

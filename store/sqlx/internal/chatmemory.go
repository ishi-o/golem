package sqlx

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/cloudwego/eino/schema"
)

type memoryRow struct {
	ConversationID string `db:"conversation_id"`
	MessagesJSON   string `db:"messages_json"`
}

func (s *Store) Append(ctx context.Context, conversationID string, messages []*schema.Message) error {
	if conversationID == "" || len(messages) == 0 {
		return nil
	}
	tx, err := s.db.BeginTxx(ctx, nil)
	if err != nil {
		return wrap("begin chat memory append", err)
	}
	defer tx.Rollback()

	// Seed an absent conversation without replacing an existing one. The
	// subsequent read is then locked on databases that support row locks, so
	// concurrent appends cannot read the same old JSON and lose a turn.
	seed := fmt.Sprintf(`INSERT INTO %s (conversation_id, messages_json) VALUES (?, ?)`, s.table("chat_memory"))
	switch s.dialect {
	case DialectMySQL:
		seed += ` ON DUPLICATE KEY UPDATE conversation_id = conversation_id`
	default:
		seed += ` ON CONFLICT (conversation_id) DO NOTHING`
	}
	if _, err := tx.ExecContext(ctx, s.rebind(seed), conversationID, "[]"); err != nil {
		return wrap("seed chat memory append", err)
	}
	var current []*schema.Message
	var row memoryRow
	selectQuery := fmt.Sprintf(`SELECT conversation_id, messages_json FROM %s WHERE conversation_id = ?`, s.table("chat_memory"))
	if s.dialect == DialectPostgres || s.dialect == DialectMySQL {
		selectQuery += ` FOR UPDATE`
	}
	if err := tx.GetContext(ctx, &row, s.rebind(selectQuery), conversationID); err != nil {
		return wrap("load chat memory before append", err)
	}
	if err := json.Unmarshal([]byte(row.MessagesJSON), &current); err != nil {
		return fmt.Errorf("decode chat memory: %w", err)
	}
	current = append(current, messages...)
	payload, err := json.Marshal(current)
	if err != nil {
		return fmt.Errorf("encode chat memory: %w", err)
	}
	update := fmt.Sprintf(`UPDATE %s SET messages_json = ? WHERE conversation_id = ?`, s.table("chat_memory"))
	if _, err := tx.ExecContext(ctx, s.rebind(update), string(payload), conversationID); err != nil {
		return wrap("append chat memory", err)
	}
	return wrap("commit chat memory append", tx.Commit())
}

func (s *Store) Load(ctx context.Context, conversationID string, window int) ([]*schema.Message, error) {
	var row memoryRow
	query := fmt.Sprintf(`SELECT conversation_id, messages_json FROM %s WHERE conversation_id = ?`, s.table("chat_memory"))
	if err := s.db.GetContext(ctx, &row, s.rebind(query), conversationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return []*schema.Message{}, nil
		}
		return nil, wrap("load chat memory", err)
	}
	var messages []*schema.Message
	if err := json.Unmarshal([]byte(row.MessagesJSON), &messages); err != nil {
		return nil, fmt.Errorf("decode chat memory: %w", err)
	}
	if window > 0 && len(messages) > window {
		messages = messages[len(messages)-window:]
	}
	return messages, nil
}

func (s *Store) Delete(ctx context.Context, conversationID string) error {
	query := fmt.Sprintf(`DELETE FROM %s WHERE conversation_id = ?`, s.table("chat_memory"))
	_, err := s.db.ExecContext(ctx, s.rebind(query), conversationID)
	return wrap("delete chat memory", err)
}

func (s *Store) ListConversations(ctx context.Context) ([]string, error) {
	var rows []struct {
		ID string `db:"conversation_id"`
	}
	query := fmt.Sprintf(`SELECT conversation_id FROM %s ORDER BY conversation_id`, s.table("chat_memory"))
	if err := s.db.SelectContext(ctx, &rows, query); err != nil {
		return nil, wrap("list chat conversations", err)
	}
	ids := make([]string, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids, nil
}

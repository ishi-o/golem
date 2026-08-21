// Package chatmemory is the persistence contract for conversation history.
//
// Eino does not prescribe a persistence implementation, so the contract stays
// in core and each adapter owns its storage details. The repository stores
// every message kind, including tool calls, so an asking turn remains ordinary
// conversation history and the next run can see it.
//
// Like the persistence contracts, implementations live in the persistence modules and
// share the database with the domain records.
package chatmemory

import (
	"context"

	"github.com/cloudwego/eino/schema"
)

// Repository stores conversation history as eino messages, addressed by
// conversation id. A conversation groups the runs that share chat memory —
// for Feishu a root message id, for the CLI a session id — so it is the unit
// a "new conversation" resets.
type Repository interface {
	// Append adds messages to the end of a conversation. Implementations must
	// preserve order and message kind (system, user, assistant, tool), since
	// a replayed window is fed straight back into the model.
	Append(ctx context.Context, conversationID string, messages []*schema.Message) error

	// Load returns the most recent `window` messages of a conversation, oldest
	// first, or all of them when window <= 0. An unknown conversation is an
	// empty slice, not an error: every new conversation is unknown the first
	// time it is loaded.
	Load(ctx context.Context, conversationID string, window int) ([]*schema.Message, error)

	// Delete removes a conversation entirely.
	Delete(ctx context.Context, conversationID string) error

	// ListConversations returns every conversation id that has history, for
	// the surfaces that let a user pick up an old thread.
	ListConversations(ctx context.Context) ([]string, error)
}

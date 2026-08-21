package store

import "context"

// The persistence contracts: backend-neutral interfaces, deliberately narrower
// than a generic CRUD surface, each method something a caller actually uses.
// Every persistence module implements all of them plus the chat memory store
// in core/chatmemory, against its own storage layout.
//
// The contracts avoid generic CRUD and backend-specific query naming. Each
// adapter implements exactly the operations the runtime uses; filtered Redis
// operations maintain explicit index sets.

// Backend bundles the persistence contracts an application needs. A selected
// adapter supplies one value, so the runtime cannot be half constructed.
type Backend interface {
	MCPServerConfigs() MCPServerConfigStore
	PendingQuestions() PendingQuestionStore
	PublishedResources() PublishedResourceStore
	ScheduledTasks() ScheduledTaskStore
	ShellCredentials() ShellCredentialStore
	ProcessedMessages() ProcessedMessageStore
}

// MCPServerConfigStore stores MCP server registrations.
type MCPServerConfigStore interface {
	Save(ctx context.Context, config MCPServerConfig) error

	ListByOwner(ctx context.Context, ownerID string) ([]MCPServerConfig, error)

	GetByOwnerAndName(ctx context.Context, ownerID, name string) (*MCPServerConfig, error)

	ExistsByOwnerAndName(ctx context.Context, ownerID, name string) (bool, error)

	DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error

	// ListSharedWith returns servers shared (directly or via a chat) with
	// any of the given identifiers.
	ListSharedWith(ctx context.Context, identifiers []string) ([]MCPServerConfig, error)

	// ListAccessibleTo returns servers owned by ownerID, plus any shared with
	// one of identifiers. Not expressible as one primitive on either Redis or
	// SQL-with-a-JSON-column, so each backend spells it out in its own query
	// language.
	ListAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]MCPServerConfig, error)
}

// PendingQuestionStore stores the outstanding asks.
type PendingQuestionStore interface {
	Save(ctx context.Context, question PendingQuestion) error

	Get(ctx context.Context, id string) (*PendingQuestion, error)

	ListByConversationAndStatus(ctx context.Context, conversationID string, status PendingQuestionStatus) ([]PendingQuestion, error)

	// SetStatus is a partial update, not a read-modify-write: the callers
	// race other answer paths and must not write stale rows back over them.
	// On Redis it also rewrites the status index entry, which a naive field
	// write would silently skip and ListByConversationAndStatus depends on.
	SetStatus(ctx context.Context, id string, status PendingQuestionStatus) error
}

// PublishedResourceStore is the one contract with nothing but id access; the
// unguessable id is the whole authorization for reaching a resource.
type PublishedResourceStore interface {
	Save(ctx context.Context, resource PublishedResource) error

	Get(ctx context.Context, id string) (*PublishedResource, error)

	Delete(ctx context.Context, id string) error
}

// ScheduledTaskStore stores the tasks the scheduler fires.
type ScheduledTaskStore interface {
	Save(ctx context.Context, task ScheduledTask) error

	Get(ctx context.Context, id string) (*ScheduledTask, error)

	ListByStatus(ctx context.Context, status ScheduledTaskStatus) ([]ScheduledTask, error)

	ListByUserAndStatus(ctx context.Context, userID string, status ScheduledTaskStatus) ([]ScheduledTask, error)

	// SetStatus is a partial update for the same reason as
	// PendingQuestionStore.SetStatus: the scheduler and the agent's cancel
	// tool both hold a copy of the task and must not write the rest of its
	// fields back over a concurrent update.
	SetStatus(ctx context.Context, id string, status ScheduledTaskStatus) error
}

// ShellCredentialStore stores secrets for sandboxed shells to receive. The
// owner+name pair is the natural key; see ShellCredentialID for how the Redis
// backend enforces it without a uniqueness constraint.
type ShellCredentialStore interface {
	Save(ctx context.Context, credential ShellCredential) error

	ListByOwner(ctx context.Context, ownerID string) ([]ShellCredential, error)

	GetByOwnerAndName(ctx context.Context, ownerID, name string) (*ShellCredential, error)

	DeleteByOwnerAndName(ctx context.Context, ownerID, name string) error
}

// ProcessedMessageStore is the cross-replica duplicate-message guard. See
// ProcessedMessage for what it defends against.
type ProcessedMessageStore interface {
	// Claim reports whether this caller was the first to take up the message.
	// It must be atomic across replicas and must never expire: a claim that
	// lapsed would let a redelivery arriving after it be answered a second
	// time, which is exactly the failure the claim exists to prevent. Each
	// backend builds it from a primitive conditional write — INSERT ... ON
	// CONFLICT DO NOTHING, a duplicate-key insert, SET NX — rather than any
	// read-then-write, which would race two replicas claiming together.
	//
	// A caller whose processing fails after claiming must Release, or the
	// retry the channel will make finds the claim held and the message is
	// dropped rather than answered.
	Claim(ctx context.Context, id string) (bool, error)

	// Release gives up a claim, for the nothing-was-done case only.
	Release(ctx context.Context, id string) error
}

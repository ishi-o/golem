package dao

import "context"

// The persistence contracts: backend-neutral interfaces, deliberately narrower
// than a generic CRUD surface, each method something a caller actually uses.
// Every persistence module implements all of them plus the chat memory store
// in core/chatmemory, against its own storage layout.
//
// The Go signature set drops Spring Data's naming-derived-query contract
// ("method names must stay valid derived queries on every backend"); what
// replaces it is the same discipline in plainer sight — implement exactly
// these methods, and note on the Redis backend that an index set must exist
// for any method that filters.

// Backend bundles every repository a persistence module supplies, so an
// embedder wires one value rather than six, and so a backend cannot be half
// constructed.
type Backend interface {
	McpServerConfigs() McpServerConfigRepo
	PendingQuestions() PendingQuestionRepo
	PublishedResources() PublishedResourceRepo
	ScheduledTasks() ScheduledTaskRepo
	ShellCredentials() ShellCredentialRepo
	ProcessedMessages() ProcessedMessageRepo
}

// McpServerConfigRepo stores MCP server registrations.
type McpServerConfigRepo interface {
	Save(ctx context.Context, config McpServerConfig) error

	FindByOwnerID(ctx context.Context, ownerID string) ([]McpServerConfig, error)

	FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*McpServerConfig, error)

	ExistsByOwnerIDAndName(ctx context.Context, ownerID, name string) (bool, error)

	DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error

	// FindBySharedWithIn returns servers shared (directly or via a chat) with
	// any of the given identifiers.
	FindBySharedWithIn(ctx context.Context, identifiers []string) ([]McpServerConfig, error)

	// FindAccessibleTo returns servers owned by ownerID, plus any shared with
	// one of identifiers. Not expressible as one primitive on either Redis or
	// SQL-with-a-JSON-column, so each backend spells it out in its own query
	// language.
	FindAccessibleTo(ctx context.Context, ownerID string, identifiers []string) ([]McpServerConfig, error)
}

// PendingQuestionRepo stores the outstanding asks.
type PendingQuestionRepo interface {
	Save(ctx context.Context, question PendingQuestion) error

	FindByID(ctx context.Context, id string) (*PendingQuestion, error)

	FindByConversationIDAndStatus(ctx context.Context, conversationID string, status PendingQuestionStatus) ([]PendingQuestion, error)

	// UpdateStatus is a partial update, not a read-modify-write: the callers
	// race other answer paths and must not write stale rows back over them.
	// On Redis it also rewrites the status index entry, which a naive field
	// write would silently skip and FindByConversationIDAndStatus depends on.
	UpdateStatus(ctx context.Context, id string, status PendingQuestionStatus) error
}

// PublishedResourceRepo is the one contract with nothing but id access; the
// unguessable id is the whole authorization for reaching a resource.
type PublishedResourceRepo interface {
	Save(ctx context.Context, resource PublishedResource) error

	FindByID(ctx context.Context, id string) (*PublishedResource, error)

	DeleteByID(ctx context.Context, id string) error
}

// ScheduledTaskRepo stores the tasks the scheduler fires.
type ScheduledTaskRepo interface {
	Save(ctx context.Context, task ScheduledTask) error

	FindByID(ctx context.Context, id string) (*ScheduledTask, error)

	FindByStatus(ctx context.Context, status ScheduledTaskStatus) ([]ScheduledTask, error)

	FindByUserIDAndStatus(ctx context.Context, userID string, status ScheduledTaskStatus) ([]ScheduledTask, error)

	// UpdateStatus is a partial update for the same reason as
	// PendingQuestionRepo.UpdateStatus: the scheduler and the agent's cancel
	// tool both hold a copy of the task and must not write the rest of its
	// fields back over a concurrent update.
	UpdateStatus(ctx context.Context, id string, status ScheduledTaskStatus) error
}

// ShellCredentialRepo stores secrets for sandboxed shells to receive. The
// owner+name pair is the natural key; see ShellCredentialID for how the Redis
// backend enforces it without a uniqueness constraint.
type ShellCredentialRepo interface {
	Save(ctx context.Context, credential ShellCredential) error

	FindByOwnerID(ctx context.Context, ownerID string) ([]ShellCredential, error)

	FindByOwnerIDAndName(ctx context.Context, ownerID, name string) (*ShellCredential, error)

	DeleteByOwnerIDAndName(ctx context.Context, ownerID, name string) error
}

// ProcessedMessageRepo is the cross-replica duplicate-message guard. See
// ProcessedMessage for what it defends against.
type ProcessedMessageRepo interface {
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

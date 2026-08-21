// Package dao holds the domain models and the persistence contracts every
// backend implements. spring-agent's one-model-per-record trick — a Java class
// carrying @Entity, @Document and @RedisHash at once, correct because an
// annotation whose type is absent at runtime is discarded on reflection — has
// no Go equivalent: there is no compileOnly, and struct tags are inert data
// with no runtime to drop them. The boundary therefore moves one level up, to
// the module graph: these structs carry no mapping at all, and each
// persistence module owns its own storage layout behind the repo interfaces
// in repo.go. A consumer adds golem/core plus one backend and inherits no
// driver belonging to the others.
//
// The structs stay anemic, one per record kind, with the id assigned by the
// application — the same deliberate choice spring-agent documents on
// ScheduledTask: a duplicate per-backend entity set plus mappers would be
// pure overhead for records this plain.
package dao

import "time"

// McpServerConfigCollection is the name every backend stores MCP server
// configs under (a SQL table, a MongoDB collection, a Redis key prefix), so
// the three schemas stay comparable at a glance. Each backend module imports
// this constant rather than spelling its own.
const McpServerConfigCollection = "mcp_servers"

// McpServerConfigDefaultVersion is the clientInfo.version reported to an MCP
// server when the config does not name one.
const McpServerConfigDefaultVersion = "1.0.0"

// SharedWithAll is a SharedWith entry meaning "every caller", not one
// specific open_id or chat_id. FindAccessibleTo's identifiers argument only
// needs to include this value for a config carrying it to become visible to
// everyone, on every backend: SharedWith is queried by plain equality or
// membership, so this sentinel needs no query changes to work.
const SharedWithAll = "*"

// McpTransport is the MCP transport a server config names. SSE was dropped
// (the upstream SDK deprecated it); only streamable HTTP remains, for now.
type McpTransport string

const McpTransportStreamableHTTP McpTransport = "STREAMABLE_HTTP"

// McpServerConfig is an MCP server a user registered, plus who may reach it.
//
// The OwnerID+Name uniqueness is declared by the two backends that can enforce
// it. Redis is not one of them — its index sets cannot express uniqueness — so
// on that backend the constraint is advisory: saving twice under the same
// owner and name with different ids yields two records rather than an error.
// Callers reach servers through FindByOwnerIDAndName, which then returns an
// arbitrary one of them.
type McpServerConfig struct {
	// ID is assigned by the application, like every model here.
	ID string

	// OwnerID is indexed by FindByOwnerID and FindByOwnerIDAndName.
	OwnerID string

	// Name is indexed for the same queries; unique per owner (see above).
	Name string

	Transport McpTransport

	URL string

	// Headers are opaque to every query, so backends may store them as a JSON
	// blob. Contrast SharedWith, which is queried and therefore deserves a
	// real index on every backend.
	Headers map[string]string

	// Title is reported to the MCP server as clientInfo.title; falls back to
	// Name.
	Title string

	// Version is reported to the MCP server as clientInfo.version; falls back
	// to McpServerConfigDefaultVersion.
	Version string

	Description string
	WebsiteURL  string

	Enabled bool

	// SharedWith holds Feishu open_ids (individual users) and/or chat_ids
	// (group chats) the owner has granted access to, in addition to the owner
	// themselves.
	//
	// Indexed per element on Redis (one set per identifier), which is what
	// lets that backend serve FindBySharedWithIn and FindAccessibleTo as
	// indexed reads rather than a scan of every stored server.
	SharedWith []string
}

// McpServerConfigAccessIdentifiers returns every SharedWith value that
// reaches the given caller: themselves, the chat they are writing from when
// there is one, and SharedWithAll. Suitable as the identifiers argument of
// both FindAccessibleTo and FindBySharedWithIn.
//
// Here rather than at either call site because there are two of them — the
// lookup that gives a run its MCP tools, and the registry tool that tells the
// user which servers they have — and a server reachable by one but invisible
// to the other is a bug either way round.
func McpServerConfigAccessIdentifiers(callerID, chatID string) []string {
	identifiers := make([]string, 0, 3)
	identifiers = append(identifiers, callerID)
	if chatID != "" {
		identifiers = append(identifiers, chatID)
	}
	identifiers = append(identifiers, SharedWithAll)
	return identifiers
}

// PendingQuestionCollection is the storage name for pending questions.
const PendingQuestionCollection = "bot_pending_questions"

// PendingQuestionStatus is the lifecycle state of a pending question.
type PendingQuestionStatus string

const (
	PendingQuestionStatusPending    PendingQuestionStatus = "PENDING"
	PendingQuestionStatusAnswered   PendingQuestionStatus = "ANSWERED"
	PendingQuestionStatusSuperseded PendingQuestionStatus = "SUPERSEDED"
	PendingQuestionStatusExpired    PendingQuestionStatus = "EXPIRED"
)

// PendingQuestion is a set of questions the agent asked and is waiting to
// hear back on. The run that asked them is long over by the time an answer
// arrives — the agent never blocks on a person — so everything needed to
// start a fresh run in the same conversation has to be written down here.
type PendingQuestion struct {
	ID string

	UserID   string
	ChatID   string
	ChatType string

	// ConversationID is indexed for FindByConversationIDAndStatus: a message
	// arriving in the conversation supersedes whatever is still unanswered in
	// it.
	ConversationID string

	RootMessageID string

	// CardID is the cardkit id of the card the form was inserted into — not
	// the id of the message that card was sent as. The answer arrives after
	// the run, and its card updater, are gone, so this is what lets the
	// callback still reach that card.
	CardID string

	// QuestionsJSON is the questions as the model phrased them, serialized.
	// Read back when the answers arrive, to turn option indexes into the
	// labels the model will recognise.
	QuestionsJSON string

	// Status is indexed alongside ConversationID, and is the property the
	// backends partially update — UpdateStatus, not a read-modify-write —
	// which keeps that index correct without rewriting the whole row.
	Status PendingQuestionStatus

	CreatedAt time.Time

	// ExpiresAt is when the questions stop being answerable. Checked when an
	// answer arrives rather than swept by a job: nothing else needs to happen
	// at that moment, so a scheduler would only be a second thing to keep
	// running.
	//
	// Bounded from above by Feishu regardless of what it is set to — a card
	// entity expires 14 days after it is created, and a form on a dead card
	// cannot be answered.
	ExpiresAt time.Time
}

// PublishedResourceCollection is the storage name for published resources.
const PublishedResourceCollection = "bot_published_resources"

// Visibility is who a published resource may be reached by.
type Visibility string

const (
	VisibilityInternal Visibility = "INTERNAL"
	VisibilityPublic   Visibility = "PUBLIC"
)

// VisibilityFrom parses a visibility, case-insensitively; ok is false for an
// unknown value. The share handler treats an unknown visibility in the path
// as a plain 404 rather than a 400, so it stays a parser and not an error
// source.
func VisibilityFrom(value string) (Visibility, bool) {
	// Case-insensitive like the Java enum's from(String).
	for _, v := range []Visibility{VisibilityInternal, VisibilityPublic} {
		if equalFold(string(v), value) {
			return v, true
		}
	}
	return "", false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// PublishedResource is a file the agent published for a link to point at.
// Nothing here is indexed: this is the one model reached only by id, the id
// being the unguessable token in the share URL.
type PublishedResource struct {
	// ID is the token in the share URL, assigned by the application.
	ID         string
	OwnerID    string
	Visibility Visibility
	Directory  bool
	// EntryFilename names the index file served when a directory resource is
	// requested without a sub-path.
	EntryFilename string
	ExpiresAt     time.Time
}

// ScheduledTaskCollection is the storage name for scheduled tasks.
const ScheduledTaskCollection = "bot_scheduled_tasks"

// ScheduledTaskStatus is the lifecycle state of a scheduled task.
type ScheduledTaskStatus string

const (
	ScheduledTaskStatusActive    ScheduledTaskStatus = "ACTIVE"
	ScheduledTaskStatusCancelled ScheduledTaskStatus = "CANCELLED"
	ScheduledTaskStatusCompleted ScheduledTaskStatus = "COMPLETED"
	ScheduledTaskStatusFailed    ScheduledTaskStatus = "FAILED"
)

// ScheduledTask is a prompt to run at a time, or on a cron schedule.
//
// On Redis, which has no query planner, an index is not a tuning decision but
// the definition of which queries can be served at all: a property without
// one cannot be filtered on. The indexed properties are exactly UserID and
// Status — what FindByUserIDAndStatus and FindByStatus read — and each one is
// a set that every write has to maintain, so nothing else carries one.
type ScheduledTask struct {
	ID string

	// UserID is indexed for FindByUserIDAndStatus.
	UserID string

	ChatID        string
	ChatType      string
	RootMessageID string

	// TaskText is the prompt to run when the task fires.
	TaskText string

	// CronExpression and ScheduledAt are mutually exclusive: a recurring task
	// names a cron, a one-shot names an instant.
	CronExpression string
	ScheduledAt    time.Time
	ExpiresAt      time.Time

	// Background is whether a firing runs unattended, out of sight of the
	// thread the task was created in. A background firing posts no answer of
	// its own: what it did is in the log, and the user hears about it only if
	// the task itself sends a message. A foreground one, the default, streams
	// into a reply on that thread as any other run does.
	//
	// What it is for: a task that decides for itself whether anything is
	// worth saying — "check X, and only if Y send a summary to Z" — or one
	// that already sends its own message. A reply for the firing is a second
	// message on top of those, and in the "otherwise do nothing" case it is
	// the only message, which is the opposite of what was asked for.
	Background bool

	// Status is stored as its name so the value matches what the other
	// backends write and stays readable and stable if the enum is ever
	// reordered. Indexed for FindByStatus and FindByUserIDAndStatus, and the
	// property the backends partially update — which is what keeps that
	// index correct without rewriting the whole task.
	Status ScheduledTaskStatus
}

// ProcessedMessageCollection is the storage name for processed messages.
const ProcessedMessageCollection = "bot_processed_messages"

// ProcessedMessage is a message that has already been taken up, so that being
// handed it a second time does not answer it a second time.
//
// Every surface that receives messages from somewhere else can be handed the
// same one twice: a channel that has not heard back in time concludes its
// event was never delivered and sends it again, and a reconnecting long-lived
// connection can replay one. What makes that visible to the user is that a
// run is not cheap to start, so the second copy arrives while the first is
// still working and both answer.
//
// The claim has to be shared rather than held in a replica's heap, because a
// redelivery is free to arrive at a different replica than the one still
// working on the first copy.
type ProcessedMessage struct {
	// ID is the channel's own id for the message. Nothing is ever queried by
	// anything else, so there is no index here: the id is the whole of the
	// record's meaning.
	ID string

	// CreatedAt is when it was claimed. The claim never expires against it —
	// see ProcessedMessageRepo.Claim.
	CreatedAt time.Time
}

// ShellCredentialCollection is the storage name for shell credentials.
const ShellCredentialCollection = "shell_credentials"

// ShellCredentialID derives the record id from owner and name. Id-equals-
// owner-plus-name is what enforces the one-credential-per-name rule on the
// Redis backend, whose index sets cannot express uniqueness; the other two
// backends declare the unique constraint outright.
func ShellCredentialID(ownerID, name string) string { return ownerID + ":" + name }

// ShellCredential is a secret stored for a user, for a sandboxed shell to
// receive as an environment variable. The shell backends that consume them
// are not ported yet; the contract is, so the persistence backends are
// complete and the model does not have to be introduced twice.
type ShellCredential struct {
	// ID is ShellCredentialID(OwnerID, Name); kept as a field so every
	// backend stores and reads the same shape.
	ID      string
	OwnerID string
	Name    string

	// Value is the secret. Backends store it verbatim; encrypting it in
	// flight to a sandbox is the shell backend's job, not the store's —
	// the store is already the trusted component in the path.
	Value string
}

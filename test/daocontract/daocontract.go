// Package daocontract is the behaviour every persistence backend must hold,
// run once per backend — the Go equivalent of spring-agent's
// AbstractPersistenceBackendTest, which it subclassed once per backend with
// a distinct owner id to avoid unique-constraint collisions.
//
// The suite is a library, not a test of this module: the repository's central
// ./test module builds each backend fixture (a SQLite file, a Mongo container,
// a Redis container) and calls Run. A behaviour that must hold for every
// backend goes here, once, rather than into an adapter package's test.
package daocontract

import (
	"context"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Fixture is one backend under test, fresh per Run call.
type Fixture interface {
	// Backend returns the repository bundle under test.
	Backend() dao.Backend

	// Memory returns the chat memory store under test, sharing the backend's
	// database.
	Memory() chatmemory.Repository

	// Owner returns a user id unique to this fixture instance, so unique
	// constraints (owner+name) cannot collide across backends running
	// against the same server.
	Owner() string

	// Close releases the fixture's resources.
	Close() error
}

// Run asserts the whole persistence contract against a fresh fixture.
func Run(t *testing.T, f Fixture) {
	t.Run("McpServerConfig", func(t *testing.T) { testMcpServerConfig(t, f) })
	t.Run("McpServerConfigAccess", func(t *testing.T) { testMcpServerConfigAccess(t, f) })
	t.Run("PendingQuestion", func(t *testing.T) { testPendingQuestion(t, f) })
	t.Run("PublishedResource", func(t *testing.T) { testPublishedResource(t, f) })
	t.Run("ScheduledTask", func(t *testing.T) { testScheduledTask(t, f) })
	t.Run("ShellCredential", func(t *testing.T) { testShellCredential(t, f) })
	t.Run("ProcessedMessage", func(t *testing.T) { testProcessedMessage(t, f) })
	t.Run("ChatMemory", func(t *testing.T) { testChatMemory(t, f) })
}

func testMcpServerConfig(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().McpServerConfigs()

	require.NoError(t, repo.Save(ctx, dao.McpServerConfig{
		ID:         "srv-1",
		OwnerID:    owner,
		Name:       "monitoring",
		Transport:  dao.McpTransportStreamableHTTP,
		URL:        "https://mcp.example.test/mcp",
		Headers:    map[string]string{"Authorization": "Bearer x"},
		Title:      "Monitoring",
		Version:    "2.0.0",
		Enabled:    true,
		SharedWith: []string{"ou_shared", dao.SharedWithAll},
	}))

	found, err := repo.FindByOwnerIDAndName(ctx, owner, "monitoring")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "Bearer x", found.Headers["Authorization"])
	assert.Equal(t, []string{"ou_shared", dao.SharedWithAll}, found.SharedWith)

	exists, err := repo.ExistsByOwnerIDAndName(ctx, owner, "monitoring")
	require.NoError(t, err)
	assert.True(t, exists)
	other, err := repo.ExistsByOwnerIDAndName(ctx, owner, "absent")
	require.NoError(t, err)
	assert.False(t, other)

	// A second server for the same owner must not disturb the first; a
	// second owner's server must stay invisible to the first owner's
	// queries.
	require.NoError(t, repo.Save(ctx, dao.McpServerConfig{ID: "srv-2", OwnerID: owner, Name: "other", Transport: dao.McpTransportStreamableHTTP, URL: "https://x.test"}))
	require.NoError(t, repo.Save(ctx, dao.McpServerConfig{ID: "srv-3", OwnerID: f.Owner(), Name: "monitoring", Transport: dao.McpTransportStreamableHTTP, URL: "https://y.test"}))
	all, err := repo.FindByOwnerID(ctx, owner)
	require.NoError(t, err)
	require.Len(t, all, 2)

	require.NoError(t, repo.DeleteByOwnerIDAndName(ctx, owner, "monitoring"))
	// Deleting again is a no-op, not an error: the caller's intent ("make it
	// gone") already holds.
	require.NoError(t, repo.DeleteByOwnerIDAndName(ctx, owner, "monitoring"))
	all, err = repo.FindByOwnerID(ctx, owner)
	require.NoError(t, err)
	assert.Len(t, all, 1)
}

func testMcpServerConfigAccess(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	// The caller owns the first server; the later f.Owner calls create the
	// distinct owners whose servers are reachable only through sharing.
	caller := owner
	repo := f.Backend().McpServerConfigs()

	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "own-1", OwnerID: owner, Name: "own", Transport: dao.McpTransportStreamableHTTP, URL: "https://a.test"}))
	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "shr-1", OwnerID: f.Owner(), Name: "shared", Transport: dao.McpTransportStreamableHTTP, URL: "https://b.test", SharedWith: []string{caller}}))
	mustSave(t, repo.Save(ctx, repoSharedWithChat(f.Owner(), "chat-1")))
	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "pub-1", OwnerID: f.Owner(), Name: "public", Transport: dao.McpTransportStreamableHTTP, URL: "https://c.test", SharedWith: []string{dao.SharedWithAll}}))
	// A server shared with somebody else reaches nobody in this test.
	mustSave(t, repo.Save(ctx, dao.McpServerConfig{ID: "oth-1", OwnerID: f.Owner(), Name: "elsewhere", Transport: dao.McpTransportStreamableHTTP, URL: "https://d.test", SharedWith: []string{"ou_nobody"}}))

	ids := func(configs []dao.McpServerConfig) []string {
		var out []string
		for _, c := range configs {
			out = append(out, c.ID)
		}
		return out
	}

	// Ownership plus sharing through the caller id, the chat, and the
	// everyone sentinel; the identifiers list mirrors what the runtime
	// derives via dao.McpServerConfigAccessIdentifiers.
	accessible, err := repo.FindAccessibleTo(ctx, caller, dao.McpServerConfigAccessIdentifiers(caller, "chat-1"))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"own-1", "shr-1", "cht-1", "pub-1"}, ids(accessible))

	shared, err := repo.FindBySharedWithIn(ctx, dao.McpServerConfigAccessIdentifiers(caller, ""))
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"shr-1", "pub-1"}, ids(shared))
}

func repoSharedWithChat(owner, chatID string) dao.McpServerConfig {
	return dao.McpServerConfig{ID: "cht-1", OwnerID: owner, Name: "viachat", Transport: dao.McpTransportStreamableHTTP, URL: "https://e.test", SharedWith: []string{chatID}}
}

func testPendingQuestion(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().PendingQuestions()

	q := dao.PendingQuestion{
		ID:             "pq-1",
		UserID:         f.Owner(),
		ConversationID: "conv-1",
		CardID:         "card-1",
		QuestionsJSON:  `[{"question":"Which?","options":["A","B"]}]`,
		Status:         dao.PendingQuestionStatusPending,
		CreatedAt:      time.Now(),
		ExpiresAt:      time.Now().Add(time.Hour),
	}
	mustSave(t, repo.Save(ctx, q))

	found, err := repo.FindByID(ctx, "pq-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, q.QuestionsJSON, found.QuestionsJSON)
	assert.Equal(t, "card-1", found.CardID)

	pending, err := repo.FindByConversationIDAndStatus(ctx, "conv-1", dao.PendingQuestionStatusPending)
	require.NoError(t, err)
	require.Len(t, pending, 1)

	// A partial update must leave every other field alone; callers race other
	// answer paths and cannot be trusted to write the rest of the row back.
	require.NoError(t, repo.UpdateStatus(ctx, "pq-1", dao.PendingQuestionStatusAnswered))
	found, err = repo.FindByID(ctx, "pq-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, dao.PendingQuestionStatusAnswered, found.Status)
	assert.Equal(t, q.QuestionsJSON, found.QuestionsJSON)
	// What stops a double-submit from starting a second run.
	pending, err = repo.FindByConversationIDAndStatus(ctx, "conv-1", dao.PendingQuestionStatusPending)
	require.NoError(t, err)
	assert.Empty(t, pending)
}

func testPublishedResource(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().PublishedResources()

	r := dao.PublishedResource{
		ID:            "token-1",
		OwnerID:       f.Owner(),
		Visibility:    dao.VisibilityPublic,
		EntryFilename: "report.html",
		ExpiresAt:     time.Now().Add(24 * time.Hour),
	}
	mustSave(t, repo.Save(ctx, r))

	found, err := repo.FindByID(ctx, "token-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, dao.VisibilityPublic, found.Visibility)
	missing, err := repo.FindByID(ctx, "absent")
	require.NoError(t, err)
	assert.Nil(t, missing)

	require.NoError(t, repo.DeleteByID(ctx, "token-1"))
	found, err = repo.FindByID(ctx, "token-1")
	require.NoError(t, err)
	assert.Nil(t, found)
}

func testScheduledTask(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().ScheduledTasks()

	task := dao.ScheduledTask{
		ID:             "task-1",
		UserID:         owner,
		TaskText:       "check the thing",
		CronExpression: "*/5 * * * *",
		Background:     true,
		Status:         dao.ScheduledTaskStatusActive,
	}
	mustSave(t, repo.Save(ctx, task))
	mustSave(t, repo.Save(ctx, dao.ScheduledTask{ID: "task-2", UserID: owner, TaskText: "once", ScheduledAt: time.Now(), Status: dao.ScheduledTaskStatusActive}))
	mustSave(t, repo.Save(ctx, dao.ScheduledTask{ID: "task-3", UserID: f.Owner(), TaskText: "other", Status: dao.ScheduledTaskStatusActive}))
	mustSave(t, repo.Save(ctx, dao.ScheduledTask{ID: "task-4", UserID: f.Owner(), TaskText: "done", Status: dao.ScheduledTaskStatusCompleted}))

	active, err := repo.FindByStatus(ctx, dao.ScheduledTaskStatusActive)
	require.NoError(t, err)
	require.Len(t, active, 3)
	mine, err := repo.FindByUserIDAndStatus(ctx, owner, dao.ScheduledTaskStatusActive)
	require.NoError(t, err)
	require.Len(t, mine, 2)

	// The partial update the scheduler depends on: a firing, a cancel and a
	// completion can all land on the same task, none writing the others'
	// fields back.
	require.NoError(t, repo.UpdateStatus(ctx, "task-1", dao.ScheduledTaskStatusCompleted))
	found, err := repo.FindByID(ctx, "task-1")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, dao.ScheduledTaskStatusCompleted, found.Status)
	assert.Equal(t, "check the thing", found.TaskText)
	assert.Equal(t, "*/5 * * * *", found.CronExpression)
	assert.True(t, found.Background)
}

func testShellCredential(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().ShellCredentials()

	cred := dao.ShellCredential{ID: dao.ShellCredentialID(owner, "api"), OwnerID: owner, Name: "api", Value: "secret"}
	mustSave(t, repo.Save(ctx, cred))

	found, err := repo.FindByOwnerIDAndName(ctx, owner, "api")
	require.NoError(t, err)
	require.NotNil(t, found)
	assert.Equal(t, "secret", found.Value)
	all, err := repo.FindByOwnerID(ctx, owner)
	require.NoError(t, err)
	require.Len(t, all, 1)

	require.NoError(t, repo.DeleteByOwnerIDAndName(ctx, owner, "api"))
	all, err = repo.FindByOwnerID(ctx, owner)
	require.NoError(t, err)
	assert.Empty(t, all)
}

func testProcessedMessage(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().ProcessedMessages()

	claimed, err := repo.Claim(ctx, "msg-1")
	require.NoError(t, err)
	assert.True(t, claimed, "first claim lost the race against nobody")
	again, err := repo.Claim(ctx, "msg-1")
	require.NoError(t, err)
	assert.False(t, again, "second claim won; a redelivery would be answered twice")

	// Release is for the nothing-was-done case; the channel's retry must be
	// able to claim again afterwards.
	require.NoError(t, repo.Release(ctx, "msg-1"))
	third, err := repo.Claim(ctx, "msg-1")
	require.NoError(t, err)
	assert.True(t, third)
}

func testChatMemory(t *testing.T, f Fixture) {
	ctx := context.Background()
	mem := f.Memory()

	conv := "conv-" + f.Owner()
	messages := []*schema.Message{
		{Role: schema.User, Content: "hello"},
		{Role: schema.Assistant, Content: "hi", ToolCalls: []schema.ToolCall{{ID: "c1", Function: schema.FunctionCall{Name: "t", Arguments: "{}"}}}},
		{Role: schema.Tool, ToolCallID: "c1", ToolName: "t", Content: "result"},
		{Role: schema.Assistant, Content: "done"},
	}
	require.NoError(t, mem.Append(ctx, conv, messages))

	loaded, err := mem.Load(ctx, conv, 0)
	require.NoError(t, err)
	require.Len(t, loaded, len(messages))
	// Tool messages must survive the round trip — the property whose absence
	// in Spring's JDBC repository forced spring-agent's synthetic-message
	// workaround. See chatmemory's package comment.
	assert.Equal(t, schema.Tool, loaded[2].Role)
	assert.Equal(t, "c1", loaded[2].ToolCallID)
	assert.Len(t, loaded[1].ToolCalls, 1)

	// The window returns the most recent messages, oldest first.
	windowed, err := mem.Load(ctx, conv, 2)
	require.NoError(t, err)
	require.Len(t, windowed, 2)
	assert.Equal(t, "result", windowed[0].Content)

	// An unknown conversation is empty, not an error.
	fresh, err := mem.Load(ctx, "never-seen", 0)
	require.NoError(t, err)
	assert.Empty(t, fresh)

	listed, err := mem.ListConversations(ctx)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.Equal(t, conv, listed[0])

	require.NoError(t, mem.Delete(ctx, conv))
	loaded, err = mem.Load(ctx, conv, 0)
	require.NoError(t, err)
	assert.Empty(t, loaded)
}

func mustSave(t *testing.T, err error) {
	t.Helper()
	require.NoError(t, err)
}

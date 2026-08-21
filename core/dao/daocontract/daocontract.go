// Package daocontract is the behaviour every persistence backend must hold,
// run once per backend — the Go equivalent of spring-agent's
// AbstractPersistenceBackendTest, which it subclassed once per backend with
// a distinct owner id to avoid unique-constraint collisions.
//
// The suite is a library, not a test of this module: each backend module's
// test package builds its fixture (a SQLite file, a Mongo container, a Redis
// container) and calls Run. A behaviour that must hold for every backend
// goes here, once, rather than into one backend's test.
package daocontract

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/dao"
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

	if err := repo.Save(ctx, dao.McpServerConfig{
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
	}); err != nil {
		t.Fatalf("save: %v", err)
	}

	found, err := repo.FindByOwnerIDAndName(ctx, owner, "monitoring")
	if err != nil || found == nil {
		t.Fatalf("find by owner and name: %v %v", found, err)
	}
	if found.Headers["Authorization"] != "Bearer x" {
		t.Fatalf("headers did not round trip: %v", found.Headers)
	}
	if !reflect.DeepEqual(found.SharedWith, []string{"ou_shared", dao.SharedWithAll}) {
		t.Fatalf("sharedWith did not round trip: %v", found.SharedWith)
	}

	exists, err := repo.ExistsByOwnerIDAndName(ctx, owner, "monitoring")
	if err != nil || !exists {
		t.Fatalf("exists: %v %v", exists, err)
	}
	other, err := repo.ExistsByOwnerIDAndName(ctx, owner, "absent")
	if err != nil || other {
		t.Fatalf("absent server reported existing: %v %v", other, err)
	}

	// A second server for the same owner must not disturb the first; a
	// second owner's server must stay invisible to the first owner's
	// queries.
	if err := repo.Save(ctx, dao.McpServerConfig{ID: "srv-2", OwnerID: owner, Name: "other", Transport: dao.McpTransportStreamableHTTP, URL: "https://x.test"}); err != nil {
		t.Fatalf("save second: %v", err)
	}
	if err := repo.Save(ctx, dao.McpServerConfig{ID: "srv-3", OwnerID: f.Owner(), Name: "monitoring", Transport: dao.McpTransportStreamableHTTP, URL: "https://y.test"}); err != nil {
		t.Fatalf("save other owner: %v", err)
	}
	all, err := repo.FindByOwnerID(ctx, owner)
	if err != nil || len(all) != 2 {
		t.Fatalf("find by owner: %d entries, err %v", len(all), err)
	}

	if err := repo.DeleteByOwnerIDAndName(ctx, owner, "monitoring"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	// Deleting again is a no-op, not an error: the caller's intent ("make it
	// gone") already holds.
	if err := repo.DeleteByOwnerIDAndName(ctx, owner, "monitoring"); err != nil {
		t.Fatalf("second delete errored: %v", err)
	}
	if all, _ := repo.FindByOwnerID(ctx, owner); len(all) != 1 {
		t.Fatalf("delete removed more than the named server: %d left", len(all))
	}
}

func testMcpServerConfigAccess(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner, caller := f.Owner(), f.Owner()
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
	if err != nil {
		t.Fatalf("find accessible: %v", err)
	}
	if got := ids(accessible); !sameSet(got, []string{"own-1", "shr-1", "cht-1", "pub-1"}) {
		t.Fatalf("accessible set wrong: %v", got)
	}

	shared, err := repo.FindBySharedWithIn(ctx, dao.McpServerConfigAccessIdentifiers(caller, ""))
	if err != nil {
		t.Fatalf("find shared: %v", err)
	}
	if got := ids(shared); !sameSet(got, []string{"shr-1", "pub-1"}) {
		t.Fatalf("shared set wrong: %v", got)
	}
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
	if err != nil || found == nil {
		t.Fatalf("find: %v %v", found, err)
	}
	if found.QuestionsJSON != q.QuestionsJSON || found.CardID != "card-1" {
		t.Fatalf("round trip lost fields: %+v", found)
	}

	pending, err := repo.FindByConversationIDAndStatus(ctx, "conv-1", dao.PendingQuestionStatusPending)
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending query: %d, err %v", len(pending), err)
	}

	// A partial update must leave every other field alone; callers race other
	// answer paths and cannot be trusted to write the rest of the row back.
	if err := repo.UpdateStatus(ctx, "pq-1", dao.PendingQuestionStatusAnswered); err != nil {
		t.Fatalf("update status: %v", err)
	}
	found, _ = repo.FindByID(ctx, "pq-1")
	if found.Status != dao.PendingQuestionStatusAnswered {
		t.Fatalf("status not updated: %+v", found)
	}
	if found.QuestionsJSON != q.QuestionsJSON {
		t.Fatalf("partial update clobbered questionsJson: %+v", found)
	}
	// What stops a double-submit from starting a second run.
	pending, err = repo.FindByConversationIDAndStatus(ctx, "conv-1", dao.PendingQuestionStatusPending)
	if err != nil || len(pending) != 0 {
		t.Fatalf("answered question still pending: %d, err %v", len(pending), err)
	}
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
	if err != nil || found == nil || found.Visibility != dao.VisibilityPublic {
		t.Fatalf("find: %v %v", found, err)
	}
	if missing, err := repo.FindByID(ctx, "absent"); err != nil || missing != nil {
		t.Fatalf("absent token must be nil without error: %v %v", missing, err)
	}

	if err := repo.DeleteByID(ctx, "token-1"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	found, err = repo.FindByID(ctx, "token-1")
	if err != nil || found != nil {
		t.Fatalf("deleted token still resolvable: %v %v", found, err)
	}
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
	if err != nil || len(active) != 3 {
		t.Fatalf("find by status: %d, err %v", len(active), err)
	}
	mine, err := repo.FindByUserIDAndStatus(ctx, owner, dao.ScheduledTaskStatusActive)
	if err != nil || len(mine) != 2 {
		t.Fatalf("find by user and status: %d, err %v", len(mine), err)
	}

	// The partial update the scheduler depends on: a firing, a cancel and a
	// completion can all land on the same task, none writing the others'
	// fields back.
	if err := repo.UpdateStatus(ctx, "task-1", dao.ScheduledTaskStatusCompleted); err != nil {
		t.Fatalf("update status: %v", err)
	}
	found, err := repo.FindByID(ctx, "task-1")
	if err != nil || found == nil {
		t.Fatalf("find after update: %v %v", found, err)
	}
	if found.Status != dao.ScheduledTaskStatusCompleted {
		t.Fatalf("status not updated: %+v", found)
	}
	if found.TaskText != "check the thing" || found.CronExpression != "*/5 * * * *" || !found.Background {
		t.Fatalf("partial update clobbered fields: %+v", found)
	}
}

func testShellCredential(t *testing.T, f Fixture) {
	ctx := context.Background()
	owner := f.Owner()
	repo := f.Backend().ShellCredentials()

	cred := dao.ShellCredential{ID: dao.ShellCredentialID(owner, "api"), OwnerID: owner, Name: "api", Value: "secret"}
	mustSave(t, repo.Save(ctx, cred))

	found, err := repo.FindByOwnerIDAndName(ctx, owner, "api")
	if err != nil || found == nil || found.Value != "secret" {
		t.Fatalf("find by owner and name: %v %v", found, err)
	}
	all, err := repo.FindByOwnerID(ctx, owner)
	if err != nil || len(all) != 1 {
		t.Fatalf("find by owner: %d, err %v", len(all), err)
	}

	if err := repo.DeleteByOwnerIDAndName(ctx, owner, "api"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if all, _ := repo.FindByOwnerID(ctx, owner); len(all) != 0 {
		t.Fatal("credential survived delete")
	}
}

func testProcessedMessage(t *testing.T, f Fixture) {
	ctx := context.Background()
	repo := f.Backend().ProcessedMessages()

	claimed, err := repo.Claim(ctx, "msg-1")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if !claimed {
		t.Fatal("first claim lost the race against nobody")
	}
	again, err := repo.Claim(ctx, "msg-1")
	if err != nil {
		t.Fatalf("second claim errored: %v", err)
	}
	if again {
		t.Fatal("second claim won; a redelivery would be answered twice")
	}

	// Release is for the nothing-was-done case; the channel's retry must be
	// able to claim again afterwards.
	if err := repo.Release(ctx, "msg-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	third, err := repo.Claim(ctx, "msg-1")
	if err != nil || !third {
		t.Fatalf("claim after release: %v %v", third, err)
	}
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
	if err := mem.Append(ctx, conv, messages); err != nil {
		t.Fatalf("append: %v", err)
	}

	loaded, err := mem.Load(ctx, conv, 0)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(loaded) != len(messages) {
		t.Fatalf("load returned %d of %d messages", len(loaded), len(messages))
	}
	// Tool messages must survive the round trip — the property whose absence
	// in Spring's JDBC repository forced spring-agent's synthetic-message
	// workaround. See chatmemory's package comment.
	if loaded[2].Role != schema.Tool || loaded[2].ToolCallID != "c1" {
		t.Fatalf("tool message lost: %+v", loaded[2])
	}
	if len(loaded[1].ToolCalls) != 1 {
		t.Fatalf("assistant tool calls lost: %+v", loaded[1])
	}

	// The window returns the most recent messages, oldest first.
	windowed, err := mem.Load(ctx, conv, 2)
	if err != nil || len(windowed) != 2 || windowed[0].Content != "result" {
		t.Fatalf("windowed load wrong: %d messages, err %v", len(windowed), err)
	}

	// An unknown conversation is empty, not an error.
	fresh, err := mem.Load(ctx, "never-seen", 0)
	if err != nil || len(fresh) != 0 {
		t.Fatalf("unknown conversation: %d messages, err %v", len(fresh), err)
	}

	listed, err := mem.ListConversations(ctx)
	if err != nil || len(listed) != 1 || listed[0] != conv {
		t.Fatalf("list conversations: %v, err %v", listed, err)
	}

	if err := mem.Delete(ctx, conv); err != nil {
		t.Fatalf("delete: %v", err)
	}
	loaded, _ = mem.Load(ctx, conv, 0)
	if len(loaded) != 0 {
		t.Fatalf("conversation survived delete: %d messages", len(loaded))
	}
}

func mustSave(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("save: %v", err)
	}
}

func sameSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	set := map[string]bool{}
	for _, s := range got {
		set[s] = true
	}
	for _, s := range want {
		if !set[s] {
			return false
		}
	}
	return true
}

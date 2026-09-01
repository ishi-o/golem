package agent_test

import (
	"context"
	"os"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/eino/components/retriever"
	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/knowledge"
	"github.com/ishi-o/golem/core/storage"
	coretools "github.com/ishi-o/golem/core/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKnowledgeFacadeScopesAndRetrieval(t *testing.T) {
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	base := knowledge.NewInMemory(knowledge.InMemoryOptions{
		ChunkSize: 32, ChunkOverlap: 4, Clock: func() time.Time { return now },
	})
	alice := knowledge.NewScope("alice", "team-a", "company-a")

	_, err := base.Index(context.Background(), knowledge.NewTextSource(alice, knowledge.TargetOwn, "Personal", "private launch plan", "", "personal"))
	require.NoError(t, err)
	_, err = base.Index(context.Background(), knowledge.NewTextSource(alice, knowledge.TargetGroup, "Team", "shared deployment runbook", "", "team"))
	require.NoError(t, err)
	_, err = base.Index(context.Background(), knowledge.NewTextSource(alice, knowledge.TargetTenant, "Company", "company retention policy", "", "company"))
	require.NoError(t, err)

	page, err := base.List(context.Background(), alice, 0, 10)
	require.NoError(t, err)
	require.Len(t, page.Entries, 3)
	assert.False(t, page.HasMore)

	// A blank group or tenant is omitted from the OR predicate; it does not
	// become a wildcard that exposes every shared document.
	other := knowledge.NewScope("bob", "", "")
	page, err = base.List(context.Background(), other, 0, 10)
	require.NoError(t, err)
	assert.Empty(t, page.Entries)
	passages, err := base.Search(context.Background(), other, "deployment runbook", 10)
	require.NoError(t, err)
	assert.Empty(t, passages)

	passages, err = base.Search(context.Background(), alice, "deployment runbook", 1)
	require.NoError(t, err)
	require.Len(t, passages, 1)
	assert.Equal(t, "team", passages[0].ID)
	assert.Equal(t, float64(1), passages[0].Score())

	r := knowledge.NewRetriever(base, alice, 4, func(metadata knowledge.Metadata) bool {
		return metadata.Group == "team-a"
	})
	passages, err = r.Retrieve(context.Background(), "runbook", retriever.WithTopK(1))
	require.NoError(t, err)
	require.Len(t, passages, 1)
	assert.Equal(t, "team", passages[0].ID)

	// Moving personal knowledge into a reachable group preserves the id and
	// makes it visible to another member of that group.
	entry, found, err := base.Move(context.Background(), alice, "personal", knowledge.TargetGroup)
	require.NoError(t, err)
	require.True(t, found)
	assert.Equal(t, knowledge.TargetGroup, entry.Target)
	page, err = base.List(context.Background(), otherWithGroup("bob", "team-a"), 0, 10)
	require.NoError(t, err)
	assert.Len(t, page.Entries, 2)

	// A reader without a writable scope cannot delete the personal document
	// before it is moved.
	_, err = base.Index(context.Background(), knowledge.NewTextSource(alice, knowledge.TargetOwn, "Private", "still private", "", "private"))
	require.NoError(t, err)
	require.NoError(t, base.Delete(context.Background(), other, "private"))
	page, err = base.List(context.Background(), alice, 0, 10)
	require.NoError(t, err)
	assert.Contains(t, entryIDs(page.Entries), "private")

	// A malformed target fails at the facade boundary instead of silently
	// becoming a personal document.
	_, err = base.Index(context.Background(), knowledge.NewTextSource(alice, knowledge.Target("workspace"), "Bad", "x", "", "bad"))
	assert.Error(t, err)

	text := knowledge.ContextText([]*schema.Document{{ID: "utf8", Content: "你好，世界", MetaData: map[string]any{knowledge.MetadataDocID: "utf8"}}}, 120)
	assert.True(t, utf8.ValidString(text))
	assert.Contains(t, text, "你好")
}

func TestKnowledgePathAndTools(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/note.md"
	require.NoError(t, os.WriteFile(path, []byte("local knowledge"), 0o644))
	base := knowledge.NewInMemory(knowledge.InMemoryOptions{})
	scope := knowledge.NewScope("alice", "", "")
	_, err := base.Index(context.Background(), knowledge.NewPathSource(scope, knowledge.TargetOwn, "Note", path, "note"))
	require.NoError(t, err)

	home := storage.NewUserHome(dir)
	family := knowledge.NewTools(base, home)
	ctx := coretools.UserID.With(context.Background(), "alice")
	index := findTool(t, family.List(), knowledge.ToolNameIndexKnowledge)
	_, err = index.InvokableRun(ctx, `{"title":"Remote","source":"https://example.test/a","docId":"remote"}`)
	assert.Error(t, err, "the facade must not fetch arbitrary remote URLs")

	list := findTool(t, family.List(), knowledge.ToolNameListKnowledge)
	result := invokeTool(t, list, ctx, `{}`)
	assert.Contains(t, result, `"docId":"note"`)
}

func otherWithGroup(owner, group string) knowledge.Scope {
	return knowledge.NewScope(owner, group, "")
}

func entryIDs(entries []knowledge.Entry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.DocID)
	}
	return ids
}

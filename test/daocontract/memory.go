package daocontract

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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

package agent_test

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/tools"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type nodeTestTool struct {
	name string
	run  func(context.Context, string) (string, error)
}

func (t nodeTestTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: t.name, Desc: "test tool"}, nil
}

func (t nodeTestTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	return t.run(ctx, arguments)
}

func newTestProvider(t *testing.T, options ...tools.ProviderOption) *tools.Provider {
	t.Helper()
	cfg := config.Config{}
	require.NoError(t, cfg.Normalize())
	dir := t.TempDir()
	cfg.Storage.Location = dir
	return tools.NewProvider(cfg, storage.NewWorkspaceFactory(dir), nil, nil, options...)
}

func toolCall(name, id, arguments string) schema.ToolCall {
	return schema.ToolCall{
		ID: id,
		Function: schema.FunctionCall{
			Name:      name,
			Arguments: arguments,
		},
	}
}

func TestProviderUsesEinoToolNodeForSequentialCallsAndMiddleware(t *testing.T) {
	var mu sync.Mutex
	var names []string
	var firstArguments string
	provider := newTestProvider(t, tools.WithInterceptor(tools.InterceptorFuncs{
		Before: func(_ context.Context, name, arguments string) (string, error) {
			if name == "FirstTestTool" {
				return `{"value":"rewritten"}`, nil
			}
			return arguments, nil
		},
		After: func(_ context.Context, name, _ string, result string) (string, bool, error) {
			if name == "FirstTestTool" {
				return "stopped: " + result, true, nil
			}
			return result, false, nil
		},
	}))
	provider.Register(nodeTestTool{
		name: "FirstTestTool",
		run: func(_ context.Context, arguments string) (string, error) {
			mu.Lock()
			names = append(names, "first")
			firstArguments = arguments
			mu.Unlock()
			return "first result", nil
		},
	}, nil)
	provider.Register(nodeTestTool{
		name: "SecondTestTool",
		run: func(_ context.Context, _ string) (string, error) {
			mu.Lock()
			names = append(names, "second")
			mu.Unlock()
			return "second result", nil
		},
	}, nil)

	composition, err := provider.Compose(context.Background(), tools.ComposeRequest{UserID: "user-1"})
	require.NoError(t, err)
	t.Cleanup(composition.Close)
	messages, ended, err := composition.Invoke(context.Background(), &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			toolCall("FirstTestTool", "call-1", `{"value":"original"}`),
			toolCall("SecondTestTool", "call-2", `{}`),
		},
	})

	require.NoError(t, err)
	assert.True(t, ended)
	require.Len(t, messages, 2)
	assert.Equal(t, "call-1", messages[0].ToolCallID)
	assert.Equal(t, "stopped: first result", messages[0].Content)
	assert.Equal(t, "call-2", messages[1].ToolCallID)
	assert.Equal(t, "second result", messages[1].Content)
	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"first", "second"}, names)
	assert.Equal(t, `{"value":"rewritten"}`, firstArguments)
}

func TestProviderToolNodeTurnsToolFailuresIntoToolMessages(t *testing.T) {
	provider := newTestProvider(t)
	provider.Register(nodeTestTool{
		name: "FailingTestTool",
		run: func(context.Context, string) (string, error) {
			return "", errors.New("boom")
		},
	}, nil)

	composition, err := provider.Compose(context.Background(), tools.ComposeRequest{UserID: "user-2"})
	require.NoError(t, err)
	t.Cleanup(composition.Close)
	messages, ended, err := composition.Invoke(context.Background(), &schema.Message{
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{toolCall("FailingTestTool", "call-fail", `{}`)},
	})

	require.NoError(t, err)
	assert.False(t, ended)
	require.Len(t, messages, 1)
	assert.Equal(t, "tool error: boom", messages[0].Content)
}

func TestProviderToolNodeHandlesUnknownToolsAsRecoveryMessages(t *testing.T) {
	provider := newTestProvider(t)
	composition, err := provider.Compose(context.Background(), tools.ComposeRequest{UserID: "user-3"})
	require.NoError(t, err)
	t.Cleanup(composition.Close)
	messages, ended, err := composition.Invoke(context.Background(), &schema.Message{
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{toolCall("NotOffered", "call-unknown", `{}`)},
	})

	require.NoError(t, err)
	assert.False(t, ended)
	require.Len(t, messages, 1)
	assert.Equal(t, "no tool named \"NotOffered\" is available in this run; say so and continue without it", messages[0].Content)
}

package agent_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
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
	provider := newTestProvider(t, tools.WithToolMiddleware(compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				call := *input
				if input.Name == "FirstTestTool" {
					call.Arguments = `{"value":"rewritten"}`
				}
				output, err := next(ctx, &call)
				if err != nil {
					return nil, err
				}
				if input.Name == "FirstTestTool" {
					output.Result = tools.EndTurnResult("stopped: " + output.Result)
				}
				return output, nil
			}
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

func TestProviderConvenienceInterceptorUsesEinoToolNode(t *testing.T) {
	var receivedArguments string
	provider := newTestProvider(t, tools.WithInterceptor(tools.InterceptorFuncs{
		Before: func(_ context.Context, _ string, arguments string) (string, error) {
			return `{"value":"rewritten"}`, nil
		},
		After: func(_ context.Context, _ string, arguments, result string) (string, bool, error) {
			receivedArguments = arguments
			return "after: " + result, true, nil
		},
	}))
	provider.Register(nodeTestTool{
		name: "ConvenienceTestTool",
		run: func(_ context.Context, arguments string) (string, error) {
			receivedArguments = arguments
			return "tool result", nil
		},
	}, nil)

	composition, err := provider.Compose(context.Background(), tools.ComposeRequest{UserID: "user-convenience"})
	require.NoError(t, err)
	t.Cleanup(composition.Close)
	messages, ended, err := composition.Invoke(context.Background(), &schema.Message{
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{toolCall("ConvenienceTestTool", "call-convenience", `{}`)},
	})

	require.NoError(t, err)
	assert.True(t, ended)
	require.Len(t, messages, 1)
	assert.Equal(t, "after: tool result", messages[0].Content)
	assert.Equal(t, `{"value":"rewritten"}`, receivedArguments)
}

func TestProviderLargeResponseMiddlewareUsesTheRunWorkspace(t *testing.T) {
	cfg := config.Config{}
	require.NoError(t, cfg.Normalize())
	dir := t.TempDir()
	cfg.Storage.Location = dir
	cfg.AI.GuideThreshold = 4
	provider := tools.NewProvider(cfg, storage.NewWorkspaceFactory(dir), nil, nil)
	provider.Register(nodeTestTool{
		name: "LargeTestTool",
		run: func(context.Context, string) (string, error) {
			return "12345", nil
		},
	}, nil)

	composition, err := provider.Compose(context.Background(), tools.ComposeRequest{UserID: "large-user"})
	require.NoError(t, err)
	t.Cleanup(composition.Close)
	ctx := tools.UserID.With(context.Background(), "large-user")
	messages, ended, err := composition.Invoke(ctx, &schema.Message{
		Role:      schema.Assistant,
		ToolCalls: []schema.ToolCall{toolCall("LargeTestTool", "call-large", `{}`)},
	})

	require.NoError(t, err)
	assert.False(t, ended)
	require.Len(t, messages, 1)
	assert.Contains(t, messages[0].Content, "This tool's result was 5 bytes")
	pathStart := strings.Index(messages[0].Content, "saved to ")
	require.NotEqual(t, -1, pathStart)
	pathEnd := strings.Index(messages[0].Content[pathStart:], ".txt")
	require.NotEqual(t, -1, pathEnd)
	filePath := messages[0].Content[pathStart+len("saved to ") : pathStart+pathEnd+len(".txt")]
	data, err := os.ReadFile(filePath)
	require.NoError(t, err)
	assert.Equal(t, "12345", string(data))
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

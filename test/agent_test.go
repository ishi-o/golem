package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/tools"
	"github.com/ishi-o/golem/test/mocks"
)

// fakeModel plays the model: each Stream call pops one scripted turn (a
// slice of chunks) and streams it. Tool calls in a turn drive the loop to
// execute and call again, exactly like a real model deciding to use a tool.
type fakeModel struct {
	mu    sync.Mutex
	turns [][]*schema.Message
	calls int
	seen  []*schema.Message
	tools []*schema.ToolInfo
	hold  <-chan struct{}
}

func (f *fakeModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if f.hold != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-f.hold:
		}
	}
	f.mu.Lock()
	f.calls++
	f.seen = append(f.seen, input...)
	f.mu.Unlock()
	f.mu.Lock()
	if len(f.turns) == 0 {
		f.mu.Unlock()
		return nil, fmt.Errorf("script exhausted")
	}
	turn := f.turns[0]
	f.turns = f.turns[1:]
	f.mu.Unlock()
	return schema.StreamReaderFromArray(turn), nil
}

func (f *fakeModel) Generate(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	return nil, fmt.Errorf("not implemented")
}

func (f *fakeModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	f.mu.Lock()
	f.tools = tools
	f.mu.Unlock()
	return f, nil
}

func (f *fakeModel) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeModel) toolCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.tools)
}

func (f *fakeModel) toolMessages() []*schema.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	var messages []*schema.Message
	for _, msg := range f.seen {
		if msg.Role == schema.Tool {
			messages = append(messages, msg)
		}
	}
	return messages
}

func textChunks(s string) []*schema.Message {
	words := strings.Split(s, " ")
	out := make([]*schema.Message, len(words))
	for i, w := range words {
		out[i] = &schema.Message{Role: schema.Assistant, Content: w + " "}
	}
	out = append(out, &schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{
		Usage: &schema.TokenUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15},
	}})
	return out
}

func newTestAgent(t *testing.T, m model.ToolCallingChatModel, configure ...func(*config.Config)) (*agent.Agent, *sqlxFixture) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{}
	require.NoError(t, cfg.Normalize())
	cfg.Storage.Location = dir
	for _, configure := range configure {
		configure(&cfg)
	}
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })
	backend := fixture.Backend()
	provider := tools.NewProvider(cfg, storage.NewWorkspaceFactory(dir), backend, nil)
	a := agent.New(m, fixture.Memory(), provider, cfg, agent.WithBackend(backend))
	return a, fixture
}

// waitFinished blocks until the done channel fires or the test times out.
func waitFinished(t *testing.T, done chan agent.Outcome) agent.Outcome {
	t.Helper()
	select {
	case o := <-done:
		return o
	case <-time.After(5 * time.Second):
		require.FailNow(t, "run did not finish in time")
		return ""
	}
}

func TestRunStreamsAccumulatedContentAndFinishesCompleted(t *testing.T) {
	m := &fakeModel{turns: [][]*schema.Message{textChunks("hello there, this is the answer")}}
	a, backend := newTestAgent(t, m)

	var mu sync.Mutex
	var contents []string
	done := make(chan agent.Outcome, 1)
	listener := agent.ListenerFuncs{
		OnContentFunc: func(soFar string) {
			mu.Lock()
			contents = append(contents, soFar)
			mu.Unlock()
		},
		OnFinishedFunc: func(o agent.Outcome) { done <- o },
	}
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "hi",
		agent.WithRequestID("run-1"),
		agent.WithIdentity("user-1", "chat-1", "p2p"),
		agent.WithConversation("conv-1", "root-1", "msg-1"),
		agent.WithListener(listener),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done))
	assert.Positive(t, m.toolCount(), "ToolCallingChatModel was not given the composed tools")

	// OnContent is accumulated: every callback is a prefix of the final.
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, contents, "no OnContent callbacks")
	for _, c := range contents {
		assert.True(t,
			strings.HasPrefix("hello there, this is the answer ", c) || strings.HasPrefix("hello there, this is the answer", c),
			"non-accumulated content callback: %q", c)
	}

	// The conversation now holds the user message and the answer.
	history, err := backend.Memory().Load(context.Background(), "conv-1", 0)
	require.NoError(t, err)
	require.Len(t, history, 2)
	assert.Equal(t, schema.User, history[0].Role)
	assert.Equal(t, "hi", history[0].Content)
	assert.Contains(t, history[1].Content, "hello")
}

func TestRunReportsModelFailure(t *testing.T) {
	ctrl := gomock.NewController(t)
	m := mocks.NewMockToolCallingChatModel(ctrl)
	modelErr := errors.New("model unavailable")
	m.EXPECT().WithTools(gomock.Any()).Return(m, nil)
	m.EXPECT().Stream(gomock.Any(), gomock.Any()).Return(nil, modelErr)
	a, _ := newTestAgent(t, m)

	done := make(chan agent.Outcome, 1)
	errs := make(chan error, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "hello",
		agent.WithIdentity("user-error", "chat-error", "p2p"),
		agent.WithListener(agent.ListenerFuncs{
			OnErrorFunc:    func(err error) { errs <- err },
			OnFinishedFunc: func(o agent.Outcome) { done <- o },
		}),
	)))

	require.Equal(t, agent.OutcomeFailed, waitFinished(t, done))
	select {
	case err := <-errs:
		require.ErrorIs(t, err, modelErr)
	default:
		require.FailNow(t, "model failure was not reported")
	}
}

func TestRunExecutesToolCallsAndContinues(t *testing.T) {
	m := &fakeModel{turns: [][]*schema.Message{
		{{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID: "call-1",
				Function: schema.FunctionCall{
					Name:      tools.ToolNameCurrentDateTime,
					Arguments: "{}",
				},
			}},
		}},
		textChunks("the time is now known"),
	}}
	a, _ := newTestAgent(t, m)
	done := make(chan agent.Outcome, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "what time is it?",
		agent.WithIdentity("user-2", "chat-2", "p2p"),
		agent.WithConversation("conv-2", "root-2", "msg-2"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(o agent.Outcome) { done <- o }}),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done))
	assert.Equal(t, 2, m.callCount(), "tool loop did not continue")
	// The second call saw the tool result message.
	toolMsgs := m.toolMessages()
	require.Len(t, toolMsgs, 1)
	assert.Equal(t, "call-1", toolMsgs[0].ToolCallID)
	assert.Contains(t, toolMsgs[0].Content, "dateTime")
}

func TestCancelEndsRunCancelled(t *testing.T) {
	release := make(chan struct{})
	m := &fakeModel{turns: [][]*schema.Message{{
		{Role: schema.Assistant, Content: "working... "},
		{Role: schema.Assistant, Content: "still working"},
	}}}
	m.hold = release
	a, _ := newTestAgent(t, m)
	done := make(chan agent.Outcome, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "slow thing",
		agent.WithRequestID("cancel-me"),
		agent.WithIdentity("user-3", "chat-3", "p2p"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(o agent.Outcome) { done <- o }}),
	)))
	require.True(t, a.Cancel("cancel-me"), "cancel did not find the run")
	require.Equal(t, agent.OutcomeCancelled, waitFinished(t, done))
	close(release)
}

func TestSyncAskContinuesAsyncAskEndsTurn(t *testing.T) {
	questions := tools.Questions{Questions: []tools.Question{{
		Question: "Which one?", Options: []string{"safe", "risky"},
	}}}

	// Synchronous: the handler answers inline, the turn continues to a
	// second model call that sees the answer.
	inline := &inlineHandler{answers: map[string]string{"Which one?": "safe"}}
	m := &fakeModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID: "ask-1", Function: schema.FunctionCall{Name: tools.ToolNameAskUserQuestion, Arguments: marshalQuestions(t, questions)},
		}}}},
		textChunks("doing the safe thing"),
	}}
	a, _ := newTestAgent(t, m, func(cfg *config.Config) {
		cfg.AI.Tools.AskUserQuestion.Enabled = true
	})
	done := make(chan agent.Outcome, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "do the thing",
		agent.WithIdentity("user-4", "chat-4", "p2p"),
		agent.WithConversation("conv-4", "root-4", "msg-4"),
		agent.WithListener(agent.ListenerFuncs{
			OnStartFunc:    func(r *agent.RunContext) { r.AddQuestionHandler(inline) },
			OnFinishedFunc: func(o agent.Outcome) { done <- o },
		}),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done))
	assert.Equal(t, 2, m.callCount(), "sync ask should continue the turn")

	// Asynchronous: no answer inside the run; the turn ends at the ask.
	pending := newSQLXFixture(t)
	async := &asyncHandler{repo: pending.Backend().PendingQuestions()}
	m2 := &fakeModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID: "ask-2", Function: schema.FunctionCall{Name: tools.ToolNameAskUserQuestion, Arguments: marshalQuestions(t, questions)},
		}}}},
		textChunks("never reached"),
	}}
	a2, _ := newTestAgent(t, m2, func(cfg *config.Config) {
		cfg.AI.Tools.AskUserQuestion.Enabled = true
	})
	done2 := make(chan agent.Outcome, 1)
	require.NoError(t, a2.Fire(agent.NewRequest(agent.ChatScenario, "do the other thing",
		agent.WithIdentity("user-5", "chat-5", "p2p"),
		agent.WithConversation("conv-5", "root-5", "msg-5"),
		agent.WithListener(agent.ListenerFuncs{
			OnStartFunc:    func(r *agent.RunContext) { r.AddQuestionHandler(async) },
			OnFinishedFunc: func(o agent.Outcome) { done2 <- o },
		}),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done2))
	assert.Equal(t, 1, m2.callCount(), "async ask should end the turn")
	// The pending question is recorded: the outstanding-ask guard's input.
	pendingQuestions, err := pending.Backend().PendingQuestions().ListByConversationAndStatus(context.Background(), "conv-5", "PENDING")
	require.NoError(t, err)
	require.Len(t, pendingQuestions, 1)
}

type inlineHandler struct{ answers map[string]string }

func (h *inlineHandler) Ask(_ context.Context, _ []tools.Question) (map[string]string, error) {
	return h.answers, nil
}

func (h *inlineHandler) AnswersInline() bool { return true }

type asyncHandler struct {
	repo store.PendingQuestionStore
}

func (h *asyncHandler) Ask(ctx context.Context, questions []tools.Question) (map[string]string, error) {
	// The Feishu shape: persist the ask, present it, return nothing.
	if err := h.repo.Save(ctx, pendingQuestion(questions)); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

func pendingQuestion(questions []tools.Question) store.PendingQuestion {
	// (kept dumb on purpose: the model-phrased questions serialized)
	return store.PendingQuestion{
		ID:             "pq-test",
		ConversationID: "conv-5",
		Status:         store.PendingQuestionStatusPending,
	}
}

func marshalQuestions(t *testing.T, q tools.Questions) string {
	t.Helper()
	data, err := json.Marshal(q)
	require.NoError(t, err)
	return string(data)
}

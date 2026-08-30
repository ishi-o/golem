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
	"github.com/cloudwego/eino/components/tool/utils"
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
	// adaptCall rewrites a scripted tool call's arguments before the turn is
	// streamed — used to splice in ids the scripted model could not have
	// known (the id a StartSubagent returned, say).
	adaptCall func(call *schema.ToolCall)
	// streamHook observes the context of each Stream call, for tests.
	streamHook func(ctx context.Context)
}

func (f *fakeModel) Stream(ctx context.Context, input []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if f.streamHook != nil {
		f.streamHook(ctx)
	}
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
	// Adapted outside the lock: the hook may read the model's seen messages.
	if f.adaptCall != nil {
		for _, chunk := range turn {
			for i := range chunk.ToolCalls {
				f.adaptCall(&chunk.ToolCalls[i])
			}
		}
	}
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
	seen := map[string]bool{}
	for _, msg := range f.seen {
		// Each model call's input re-includes the earlier tool results;
		// what the assertions want is one entry per tool call.
		if msg.Role == schema.Tool && !seen[msg.ToolCallID] {
			seen[msg.ToolCallID] = true
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
	return buildTestAgent(t, m, nil, configure...)
}

// buildTestAgent is newTestAgent plus a hook to register process-wide tools
// on the provider before the agent is constructed.
func buildTestAgent(t *testing.T, m model.ToolCallingChatModel, register func(p *tools.Provider), configure ...func(*config.Config)) (*agent.Agent, *sqlxFixture) {
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
	if register != nil {
		register(provider)
	}
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

// reasoningTurns scripts a run whose two model calls each stream reasoning:
// the first as deltas, the second in cumulative chunks (a provider that
// resends the whole call's thinking so far on every chunk). A tool call ends
// the first call so the loop reaches the second.
func reasoningTurns() [][]*schema.Message {
	return [][]*schema.Message{
		{
			{Role: schema.Assistant, ReasoningContent: "first "},
			{Role: schema.Assistant, ReasoningContent: "thoughts"},
			{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
				ID: "call-r", Function: schema.FunctionCall{Name: tools.ToolNameCurrentDateTime, Arguments: "{}"},
			}}},
		},
		{
			{Role: schema.Assistant, ReasoningContent: "re"},
			{Role: schema.Assistant, ReasoningContent: "reasoned twice"},
			{Role: schema.Assistant, Content: "final answer"},
		},
	}
}

func TestRunStreamsReasoningPerModelCall(t *testing.T) {
	m := &fakeModel{turns: reasoningTurns()}
	a, _ := newTestAgent(t, m)
	done := make(chan agent.Outcome, 1)
	var mu sync.Mutex
	var reasoning []string
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "think hard",
		agent.WithIdentity("user-r", "chat-r", "p2p"),
		agent.WithListener(agent.ListenerFuncs{
			OnReasoningFunc: func(soFar string) {
				mu.Lock()
				reasoning = append(reasoning, soFar)
				mu.Unlock()
			},
			OnFinishedFunc: func(o agent.Outcome) { done <- o },
		}),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done))
	require.Equal(t, 2, m.callCount(), "the scripted tool call should have driven a second model call")
	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, reasoning, "no OnReasoning callbacks")
	last := reasoning[len(reasoning)-1]
	assert.Equal(t, "first thoughts\n\nreasoned twice", last,
		"reasoning should carry one block per model call, cumulative chunks deduplicated")
	// Every callback is a prefix of the final accumulation.
	for _, r := range reasoning {
		assert.True(t, strings.HasPrefix("first thoughts\n\nreasoned twice", r),
			"non-accumulated reasoning callback: %q", r)
	}
}

func TestRunWithoutReasoningStaysSilent(t *testing.T) {
	m := &fakeModel{turns: [][]*schema.Message{textChunks("plain answer")}}
	a, _ := newTestAgent(t, m)
	done := make(chan agent.Outcome, 1)
	called := false
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "hi",
		agent.WithIdentity("user-quiet", "chat-quiet", "p2p"),
		agent.WithListener(agent.ListenerFuncs{
			OnReasoningFunc: func(string) { called = true },
			OnFinishedFunc:  func(o agent.Outcome) { done <- o },
		}),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done))
	assert.False(t, called, "OnReasoning fired for a model that sent none")
}

// blockingTool is a registered tool that signals when the run is inside it
// and blocks until released — the seam the queueing tests use to offer a
// message while the run is mid-tool.
const blockingToolName = "BlockingTestTool"

// newBlockingTestAgent builds an agent with the blocking tool registered.
// entered may be nil.
func newBlockingTestAgent(t *testing.T, m *fakeModel, entered, release chan struct{}) *agent.Agent {
	t.Helper()
	a, _ := buildTestAgent(t, m, func(p *tools.Provider) {
		p.Register(tools.MustTool(utils.InferTool(blockingToolName,
			"Blocks until released; for tests.",
			func(ctx context.Context, _ struct{}) (string, error) {
				if entered != nil {
					entered <- struct{}{}
				}
				if release != nil {
					<-release
				}
				return "unblocked", nil
			})), nil)
	})
	return a
}

func TestFireOrQueueJoinsRunAtToolBoundary(t *testing.T) {
	// The first model call runs a tool that blocks until the second message
	// has been offered, so the join is deterministic.
	midTool := make(chan struct{})
	release := make(chan struct{})
	m := &fakeModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID: "call-q", Function: schema.FunctionCall{Name: blockingToolName, Arguments: "{}"},
		}}}},
		textChunks("taking the addition into account"),
	}}
	a := newBlockingTestAgent(t, m, midTool, release)

	done := make(chan agent.Outcome, 1)
	var mu sync.Mutex
	var queued, read []string
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "start the work",
		agent.WithRequestID("run-q"),
		agent.WithIdentity("user-q", "chat-q", "p2p"),
		agent.WithConversation("conv-q", "root-q", "msg-q"),
		agent.WithListener(agent.ListenerFuncs{
			OnMessageQueuedFunc: func(id, _ string) {
				mu.Lock()
				queued = append(queued, id)
				mu.Unlock()
			},
			OnQueuedMessageReadFunc: func(ids []string) {
				mu.Lock()
				read = append(read, ids...)
				mu.Unlock()
			},
			OnFinishedFunc: func(o agent.Outcome) { done <- o },
		}),
	)))
	// Wait until the run is inside the tool, then offer a message to it.
	<-midTool
	joined := a.FireOrQueue(agent.NewRequest(agent.ChatScenario, "also do this",
		agent.WithRequestID("run-q-2"),
		agent.WithIdentity("user-q", "chat-q", "p2p"),
		agent.WithConversation("conv-q", "root-q", "msg-q-2"),
	), func() string { return "also do this" }, "also do this")
	require.True(t, joined, "the live run should have taken the message")
	close(release)
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done))

	mu.Lock()
	defer mu.Unlock()
	assert.Equal(t, []string{"run-q-2"}, queued, "no queued notification")
	assert.Equal(t, []string{"run-q-2"}, read, "no read notification")
	// The last model call saw the joined message as a user message.
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.seen)
	var late *schema.Message
	for _, msg := range m.seen {
		if msg.Role == schema.User && strings.Contains(msg.Content, "also do this") {
			late = msg
		}
	}
	require.NotNil(t, late, "the joined message never reached the model")
}

func TestFireOrQueueUnreadRefiresAsOwnRun(t *testing.T) {
	entered := make(chan struct{})
	releaseTool := make(chan struct{})
	releaseStream := make(chan struct{})
	m := &fakeModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID: "call-r", Function: schema.FunctionCall{Name: blockingToolName, Arguments: "{}"},
		}}}},
		textChunks("answered the first message"),
		// Consumed by the re-fired run for the unread message.
		textChunks("answered the second message too"),
	}}
	a := newBlockingTestAgent(t, m, entered, releaseTool)

	finished := make(chan agent.Outcome, 2)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "first",
		agent.WithRequestID("run-refire"),
		agent.WithIdentity("user-r", "chat-r", "p2p"),
		agent.WithConversation("conv-r", "root-r", "msg-r"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(o agent.Outcome) { finished <- o }}),
	)))
	<-entered
	// Hold the run's second model call in the stream, so the second message
	// arrives after the last tool boundary: nothing will read it.
	m.mu.Lock()
	m.hold = releaseStream
	m.mu.Unlock()
	close(releaseTool)
	// Give the boundary read and the held stream call a moment, then offer.
	time.Sleep(50 * time.Millisecond)
	require.True(t, a.FireOrQueue(agent.NewRequest(agent.ChatScenario, "second",
		agent.WithRequestID("run-refire-2"),
		agent.WithIdentity("user-r", "chat-r", "p2p"),
		agent.WithConversation("conv-r", "root-r", "msg-r-2"),
		// The listener rides the request, so the re-fired run reports too.
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(o agent.Outcome) { finished <- o }}),
	), func() string { return "the second message" }, "second"))
	close(releaseStream)

	// The first run finishes, then the unread message runs as its own run —
	// two finishes, and the re-fired run sees the message text.
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, finished))
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, finished))
	m.mu.Lock()
	defer m.mu.Unlock()
	require.Equal(t, 3, m.calls, "the unread message should have run as its own model conversation")
	var saw bool
	for _, msg := range m.seen {
		// The re-fired run carries the request's own Text; the producer body
		// is only for reading into a live run.
		if msg.Role == schema.User && msg.Content == "second" {
			saw = true
		}
	}
	assert.True(t, saw, "the re-fired message never reached a model")
}

func TestFireOrQueueIgnoresOtherUsersAndConversations(t *testing.T) {
	midTool := make(chan struct{})
	release := make(chan struct{})
	m := &fakeModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID: "call-x", Function: schema.FunctionCall{Name: blockingToolName, Arguments: "{}"},
		}}}},
		textChunks("done"),
	}}
	a := newBlockingTestAgent(t, m, midTool, release)
	done := make(chan agent.Outcome, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "work",
		agent.WithRequestID("run-x"),
		agent.WithIdentity("user-x", "chat-x", "p2p"),
		agent.WithConversation("conv-x", "root-x", "msg-x"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(o agent.Outcome) { done <- o }}),
	)))
	<-midTool
	// Same conversation, different user.
	assert.False(t, a.FireOrQueue(agent.NewRequest(agent.ChatScenario, "aside",
		agent.WithRequestID("run-y"),
		agent.WithIdentity("user-y", "chat-x", "p2p"),
		agent.WithConversation("conv-x", "root-x", "msg-y"),
	), func() string { return "aside" }, "aside"), "another user's message must not join")
	// Same user, different conversation.
	assert.False(t, a.FireOrQueue(agent.NewRequest(agent.ChatScenario, "elsewhere",
		agent.WithRequestID("run-z"),
		agent.WithIdentity("user-x", "chat-z", "p2p"),
		agent.WithConversation("conv-z", "root-z", "msg-z"),
	), func() string { return "elsewhere" }, "elsewhere"), "another conversation's message must not join")
	close(release)
	require.Equal(t, agent.OutcomeCompleted, waitFinished(t, done))
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

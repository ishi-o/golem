package agent_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/dao/inmemory"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/tools"
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

func (f *fakeModel) Info(_ context.Context) (*schema.ToolInfo, error) { return nil, nil }

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

func newTestAgent(t *testing.T, m *fakeModel) (*agent.Agent, *inmemory.Backend) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	cfg.Storage.Location = dir
	backend := inmemory.New()
	provider := tools.NewProvider(cfg, storage.NewWorkspaceFactory(dir), backend, nil)
	a := agent.New(m, backend.Memory(), provider, cfg)
	a.Repos = backend
	return a, backend
}

// waitFinished blocks until the done channel fires or the test times out.
func waitFinished(t *testing.T, done chan agent.Outcome) agent.Outcome {
	t.Helper()
	select {
	case o := <-done:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("run did not finish in time")
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
		OnContentF: func(soFar string) {
			mu.Lock()
			contents = append(contents, soFar)
			mu.Unlock()
		},
		OnFinishedF: func(o agent.Outcome) { done <- o },
	}
	if err := a.Fire(agent.NewRequest(agent.ChatScenario, "hi",
		agent.WithRequestID("run-1"),
		agent.WithIdentity("user-1", "chat-1", "p2p"),
		agent.WithConversation("conv-1", "root-1", "msg-1"),
		agent.WithListener(listener),
	)); err != nil {
		t.Fatal(err)
	}
	if outcome := waitFinished(t, done); outcome != agent.OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	m.mu.Lock()
	toolCount := len(m.tools)
	m.mu.Unlock()
	if toolCount == 0 {
		t.Fatal("ToolCallingChatModel was not given the composed tools")
	}

	// OnContent is accumulated: every callback is a prefix of the final.
	mu.Lock()
	defer mu.Unlock()
	if len(contents) == 0 {
		t.Fatal("no OnContent callbacks")
	}
	for _, c := range contents {
		if !strings.HasPrefix("hello there, this is the answer ", c) && !strings.HasPrefix("hello there, this is the answer", c) {
			t.Fatalf("non-accumulated content callback: %q", c)
		}
	}

	// The conversation now holds the user message and the answer.
	history, err := backend.Memory().Load(context.Background(), "conv-1", 0)
	if err != nil || len(history) != 2 {
		t.Fatalf("history = %d messages, err %v", len(history), err)
	}
	if history[0].Role != schema.User || history[0].Content != "hi" {
		t.Fatalf("user message not persisted first: %+v", history[0])
	}
	if !strings.Contains(history[1].Content, "hello") {
		t.Fatalf("assistant answer not persisted: %+v", history[1])
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
	if err := a.Fire(agent.NewRequest(agent.ChatScenario, "what time is it?",
		agent.WithIdentity("user-2", "chat-2", "p2p"),
		agent.WithConversation("conv-2", "root-2", "msg-2"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedF: func(o agent.Outcome) { done <- o }}),
	)); err != nil {
		t.Fatal(err)
	}
	if outcome := waitFinished(t, done); outcome != agent.OutcomeCompleted {
		t.Fatalf("outcome = %s", outcome)
	}
	if m.calls != 2 {
		t.Fatalf("model called %d times, want 2 (tool loop did not continue)", m.calls)
	}
	// The second call saw the tool result message.
	m.mu.Lock()
	defer m.mu.Unlock()
	var toolMsgs []*schema.Message
	for _, msg := range m.seen {
		if msg.Role == schema.Tool {
			toolMsgs = append(toolMsgs, msg)
		}
	}
	if len(toolMsgs) != 1 || toolMsgs[0].ToolCallID != "call-1" {
		t.Fatalf("tool result missing from the model's input: %d tool messages", len(toolMsgs))
	}
	if !strings.Contains(toolMsgs[0].Content, "dateTime") {
		t.Fatalf("tool result does not look like the datetime output: %q", toolMsgs[0].Content)
	}
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
	if err := a.Fire(agent.NewRequest(agent.ChatScenario, "slow thing",
		agent.WithRequestID("cancel-me"),
		agent.WithIdentity("user-3", "chat-3", "p2p"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedF: func(o agent.Outcome) { done <- o }}),
	)); err != nil {
		t.Fatal(err)
	}
	if !a.Cancel("cancel-me") {
		t.Fatal("cancel did not find the run")
	}
	if outcome := waitFinished(t, done); outcome != agent.OutcomeCancelled {
		t.Fatalf("outcome = %s, want CANCELLED", outcome)
	}
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
	a, _ := newTestAgent(t, m)
	a.Config.AI.Tools.AskUserQuestion.Enabled = true
	done := make(chan agent.Outcome, 1)
	if err := a.Fire(agent.NewRequest(agent.ChatScenario, "do the thing",
		agent.WithIdentity("user-4", "chat-4", "p2p"),
		agent.WithConversation("conv-4", "root-4", "msg-4"),
		agent.WithListener(agent.ListenerFuncs{
			OnStartF:    func(r *agent.RunRegistry) { r.AddQuestionHandler(inline) },
			OnFinishedF: func(o agent.Outcome) { done <- o },
		}),
	)); err != nil {
		t.Fatal(err)
	}
	if outcome := waitFinished(t, done); outcome != agent.OutcomeCompleted {
		t.Fatalf("sync ask outcome = %s", outcome)
	}
	if m.calls != 2 {
		t.Fatalf("sync ask should continue the turn; model called %d times", m.calls)
	}

	// Asynchronous: no answer inside the run; the turn ends at the ask.
	pending := inmemory.New()
	async := &asyncHandler{backend: pending}
	m2 := &fakeModel{turns: [][]*schema.Message{
		{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
			ID: "ask-2", Function: schema.FunctionCall{Name: tools.ToolNameAskUserQuestion, Arguments: marshalQuestions(t, questions)},
		}}}},
		textChunks("never reached"),
	}}
	a2, _ := newTestAgent(t, m2)
	a2.Config.AI.Tools.AskUserQuestion.Enabled = true
	done2 := make(chan agent.Outcome, 1)
	if err := a2.Fire(agent.NewRequest(agent.ChatScenario, "do the other thing",
		agent.WithIdentity("user-5", "chat-5", "p2p"),
		agent.WithConversation("conv-5", "root-5", "msg-5"),
		agent.WithListener(agent.ListenerFuncs{
			OnStartF:    func(r *agent.RunRegistry) { r.AddQuestionHandler(async) },
			OnFinishedF: func(o agent.Outcome) { done2 <- o },
		}),
	)); err != nil {
		t.Fatal(err)
	}
	if outcome := waitFinished(t, done2); outcome != agent.OutcomeCompleted {
		t.Fatalf("async ask outcome = %s", outcome)
	}
	if m2.calls != 1 {
		t.Fatalf("async ask should end the turn; model called %d times", m2.calls)
	}
	// The pending question is recorded: the outstanding-ask guard's input.
	pendingQuestions, err := pending.PendingQuestions().FindByConversationIDAndStatus(context.Background(), "conv-5", "PENDING")
	if err != nil || len(pendingQuestions) != 1 {
		t.Fatalf("async ask did not record a pending question: %d, err %v", len(pendingQuestions), err)
	}
}

type inlineHandler struct{ answers map[string]string }

func (h *inlineHandler) Ask(_ context.Context, _ []tools.Question) (map[string]string, error) {
	return h.answers, nil
}

func (h *inlineHandler) AnswersInline() bool { return true }

type asyncHandler struct{ backend *inmemory.Backend }

func (h *asyncHandler) Ask(ctx context.Context, questions []tools.Question) (map[string]string, error) {
	// The Feishu shape: persist the ask, present it, return nothing.
	if err := h.backend.PendingQuestions().Save(ctx, pendingQuestion(questions)); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

func pendingQuestion(questions []tools.Question) dao.PendingQuestion {
	// (kept dumb on purpose: the model-phrased questions serialized)
	return dao.PendingQuestion{
		ID:             "pq-test",
		ConversationID: "conv-5",
		Status:         dao.PendingQuestionStatusPending,
	}
}

func marshalQuestions(t *testing.T, q tools.Questions) string {
	t.Helper()
	data, err := json.Marshal(q)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

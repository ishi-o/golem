package agent_test

import (
	"context"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/subagent"
	"github.com/ishi-o/golem/core/tools"
)

// laneModel routes each run to its own script: the subagent's user message
// is the rendered brief (it contains the marker of the default template),
// the parent's is whatever the test fired. Without the lanes, one shared
// script queue would hand turns to whichever run asked first.
type laneModel struct {
	parent *fakeModel
	child  *fakeModel
}

func (l *laneModel) isChildLane(input []*schema.Message) bool {
	for _, m := range input {
		if m.Role == schema.User && strings.Contains(m.Content, "# The brief") {
			return true
		}
	}
	return false
}

func (l *laneModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if l.isChildLane(input) {
		return l.child.Stream(ctx, input, opts...)
	}
	return l.parent.Stream(ctx, input, opts...)
}

func (l *laneModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return nil, nil
}

func (l *laneModel) WithTools(ts []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if _, err := l.parent.WithTools(ts); err != nil {
		return nil, err
	}
	if _, err := l.child.WithTools(ts); err != nil {
		return nil, err
	}
	return l, nil
}

// spliceSubagentIDs lets a scripted wait call name the subagent the start
// call actually started: "*" in subagentId is replaced by the id found in
// the tool results the run has already produced.
var subIDPattern = regexp.MustCompile(`sub_[0-9a-f]+`)

func spliceSubagentIDs(f *fakeModel) func(*schema.ToolCall) {
	return func(call *schema.ToolCall) {
		if !strings.Contains(call.Function.Arguments, `"subagentId":"*"`) {
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, m := range f.seen {
			if m.Role == schema.Tool {
				if id := subIDPattern.FindString(m.Content); id != "" {
					call.Function.Arguments = strings.Replace(
						call.Function.Arguments, `"*"`, `"`+id+`"`, 1)
					return
				}
			}
		}
	}
}

func subagentToolCall(id, name, args string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{
		ID: id, Function: schema.FunctionCall{Name: name, Arguments: args},
	}}}
}

func startCall(id int, description, prompt string) *schema.Message {
	return subagentToolCall("c-start-"+strings.Repeat("x", id), tools.ToolNameStartSubagent,
		`{"description":"`+description+`","prompt":"`+prompt+`"}`)
}

func waitCall(n int) *schema.Message {
	return subagentToolCall("c-wait-"+strings.Repeat("x", n), tools.ToolNameWaitSubagent,
		`{"subagentId":"*"}`)
}

func newSubagentTestAgent(t *testing.T, m model.ToolCallingChatModel, configure ...func(*config.Config)) (*agent.Agent, *sqlxFixture) {
	t.Helper()
	dir := t.TempDir()
	cfg := config.Config{}
	require.NoError(t, cfg.Normalize())
	cfg.Storage.Location = dir
	cfg.AI.Tools.Subagent.WaitPoll = 50 * time.Millisecond
	for _, c := range configure {
		c(&cfg)
	}
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })
	backend := fixture.Backend()
	provider := tools.NewProvider(cfg, storage.NewWorkspaceFactory(dir), backend, nil)
	a := agent.New(m, fixture.Memory(), provider, cfg, agent.WithBackend(backend))
	subagent.Register(provider, a, cfg, nil, nil)
	return a, fixture
}

func TestSubagentScenarioWithholdsSubagentAndScheduleTools(t *testing.T) {
	s := agent.SubagentScenario
	assert.False(t, s.ConversationMemory(), "a subagent joins no conversation")
	for _, name := range []string{
		tools.ToolNameStartSubagent, tools.ToolNameWaitSubagent, tools.ToolNameCancelSubagent,
		tools.ToolNameCreateScheduledTask, tools.ToolNameListScheduledTasks, tools.ToolNameCancelScheduledTask,
	} {
		assert.False(t, s.Offers(name), "SUBAGENT scenario must not offer %s", name)
	}
	assert.True(t, s.Offers(tools.ToolNameReadFile), "SUBAGENT scenario still offers the ordinary tools")
	assert.True(t, agent.ChatScenario.Offers(tools.ToolNameStartSubagent), "chat runs offer the subagent tools")
}

func TestSubagentStartWaitAndAttribution(t *testing.T) {
	lanes := &laneModel{
		parent: &fakeModel{turns: [][]*schema.Message{
			{startCall(1, "Reading the timeline", "Read the timeline and report the turning points.")},
			{waitCall(1)},
			textChunks("all done, the subagent answered"),
		}},
		child: &fakeModel{turns: [][]*schema.Message{
			textChunks("the turning points were three"),
		}},
	}
	lanes.parent.adaptCall = spliceSubagentIDs(lanes.parent)
	a, fixture := newSubagentTestAgent(t, lanes)

	var mu sync.Mutex
	var events []agent.SubagentEvent
	finishedAfterChildEnd := false
	parentFinished := make(chan struct{})
	childEnded := make(chan struct{})
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "summarize the incident",
		agent.WithRequestID("parent-1"),
		agent.WithIdentity("user-sub", "chat-sub", "p2p"),
		agent.WithConversation("conv-sub", "root-sub", "msg-sub"),
		agent.WithListener(agent.ListenerFuncs{
			OnSubagentFunc: func(e agent.SubagentEvent) {
				mu.Lock()
				events = append(events, e)
				ended := e.Ended()
				mu.Unlock()
				if ended {
					select {
					case <-childEnded:
					default:
						close(childEnded)
					}
				}
			},
			OnFinishedFunc: func(agent.Outcome) {
				// The parent may only finish once its child has reported its end.
				select {
				case <-childEnded:
					finishedAfterChildEnd = true
				default:
				}
				close(parentFinished)
			},
		}),
	)))
	<-parentFinished

	mu.Lock()
	defer mu.Unlock()
	require.NotEmpty(t, events)
	assert.True(t, events[0].Started(), "the first event should be the start: %+v", events[0])
	var ended agent.SubagentEvent
	said, spent := false, false
	for _, e := range events {
		switch {
		case e.Ended():
			ended = e
		case e.Said():
			said = true
		case e.Spent():
			spent = true
		}
	}
	require.NotEmpty(t, ended.SubagentID, "no ended event for the subagent")
	assert.Equal(t, agent.OutcomeCompleted, ended.Outcome)
	assert.Contains(t, ended.ContentSoFar, "turning points", "the ended event carries the final answer")
	assert.True(t, said, "no content-so-far event reached the parent")
	assert.True(t, spent, "the subagent's usage was not attributed to the parent")
	assert.True(t, finishedAfterChildEnd, "the parent finished before its subagent reported its end")

	// The wait returned the subagent's answer to the parent's model.
	toolMsgs := lanes.parent.toolMessages()
	require.Len(t, toolMsgs, 2)
	assert.Contains(t, toolMsgs[1].Content, "turning points", "WaitForSubagent did not hand back the answer")

	// The parent's conversation holds only the parent's messages. The
	// subagent's answer appears exactly once, as the wait tool's result —
	// never as a message of its own: its conversation is its own.
	history, err := fixture.Memory().Load(context.Background(), "conv-sub", 0)
	require.NoError(t, err)
	for _, m := range history {
		if m.Role == schema.User || m.Role == schema.Assistant {
			assert.NotContains(t, m.Content, "the turning points were three",
				"the subagent's words leaked into the parent's conversation as a %s message", m.Role)
		}
	}
}

func TestSubagentCancelCascadesFromParent(t *testing.T) {
	release := make(chan struct{})
	lanes := &laneModel{
		parent: &fakeModel{turns: [][]*schema.Message{
			{startCall(1, "Slow sweep", "Sweep everything.")},
			{{Role: schema.Assistant, Content: "meanwhile..."}},
		}},
		child: &fakeModel{turns: [][]*schema.Message{
			textChunks("child answer nobody reads"),
		}},
	}
	lanes.child.hold = release
	a, _ := newSubagentTestAgent(t, lanes)

	childEnded := make(chan agent.Outcome, 1)
	parentFinished := make(chan agent.Outcome, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "do the sweep",
		agent.WithRequestID("parent-cancel"),
		agent.WithIdentity("user-sub2", "chat-sub2", "p2p"),
		agent.WithListener(agent.ListenerFuncs{
			OnSubagentFunc: func(e agent.SubagentEvent) {
				if e.Ended() {
					select {
					case childEnded <- e.Outcome:
					default:
					}
				}
			},
			OnFinishedFunc: func(o agent.Outcome) { parentFinished <- o },
		}),
	)))
	// Give the subagent a moment to be started and held, then cancel the
	// parent — which is by then waiting on the child in its finish path.
	// Its own model work already finished, so its outcome is COMPLETED;
	// what the cancellation must reach is the child.
	time.Sleep(100 * time.Millisecond)
	require.True(t, a.Cancel("parent-cancel"))
	parentOutcome := waitOutcome(t, parentFinished)
	assert.NotEqual(t, agent.OutcomeFailed, parentOutcome)
	select {
	case o := <-childEnded:
		assert.Equal(t, agent.OutcomeCancelled, o, "cancelling the parent must cancel the subagent")
	case <-time.After(5 * time.Second):
		require.FailNow(t, "subagent did not report its end after the parent was cancelled")
	}
	close(release)
}

func TestSubagentWaitTimesOutAndCancels(t *testing.T) {
	release := make(chan struct{})
	lanes := &laneModel{
		parent: &fakeModel{turns: [][]*schema.Message{
			{startCall(1, "Stuck work", "Do something that never ends.")},
			{waitCall(1)},
			{waitCall(2)},
			{waitCall(3)},
			textChunks("gave up on it"),
		}},
		child: &fakeModel{turns: [][]*schema.Message{
			textChunks("never delivered"),
		}},
	}
	lanes.parent.adaptCall = spliceSubagentIDs(lanes.parent)
	lanes.child.hold = release
	a, _ := newSubagentTestAgent(t, lanes, func(cfg *config.Config) {
		cfg.AI.Tools.Subagent.WaitTimeout = 150 * time.Millisecond
	})
	done := make(chan agent.Outcome, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "try the stuck thing",
		agent.WithRequestID("parent-timeout"),
		agent.WithIdentity("user-sub3", "chat-sub3", "p2p"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(o agent.Outcome) { done <- o }}),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitOutcome(t, done))
	toolMsgs := lanes.parent.toolMessages()
	require.Len(t, toolMsgs, 4)
	assert.Contains(t, toolMsgs[3].Content, "cancelled",
		"a subagent past the wait ceiling should be reported cancelled: %q", toolMsgs[3].Content)
	close(release)
}

func TestSubagentEmptyPromptRefused(t *testing.T) {
	lanes := &laneModel{
		parent: &fakeModel{turns: [][]*schema.Message{
			{subagentToolCall("c-start", tools.ToolNameStartSubagent, `{"description":"nothing","prompt":""}`)},
			textChunks("fine, I will do it myself"),
		}},
		child: &fakeModel{},
	}
	a, _ := newSubagentTestAgent(t, lanes)
	done := make(chan agent.Outcome, 1)
	require.NoError(t, a.Fire(agent.NewRequest(agent.ChatScenario, "delegate",
		agent.WithRequestID("parent-empty"),
		agent.WithIdentity("user-sub4", "chat-sub4", "p2p"),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(o agent.Outcome) { done <- o }}),
	)))
	require.Equal(t, agent.OutcomeCompleted, waitOutcome(t, done))
	toolMsgs := lanes.parent.toolMessages()
	require.Len(t, toolMsgs, 1)
	assert.Contains(t, toolMsgs[0].Content, "prompt", "an empty brief should be refused with the instruction")
}

func waitOutcome(t *testing.T, ch chan agent.Outcome) agent.Outcome {
	t.Helper()
	select {
	case o := <-ch:
		return o
	case <-time.After(5 * time.Second):
		require.FailNow(t, "run did not finish in time")
		return ""
	}
}

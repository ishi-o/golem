// Package agent is the runtime: one entry point for running the agent.
// Everything after Fire — tool composition, system-prompt rendering, the
// model call, listener fan-out, cancellation — happens in here. A surface
// builds a Request and hands it over; it never touches the tools provider,
// the model, or the memory store directly.
package agent

import (
	"context"
	"sync"

	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/tools"
)

// Outcome is how a run ended.
type Outcome string

const (
	OutcomeCompleted Outcome = "COMPLETED"
	OutcomeFailed    Outcome = "FAILED"
	OutcomeCancelled Outcome = "CANCELLED"
)

// Scenario names the kind of run, and what follows from that: whether the
// run reads and writes conversation memory, and which tools it is offered.
type Scenario interface {
	// Name identifies the scenario in logs and surfaces.
	Name() string

	// ConversationMemory reports whether the run joins the conversation it
	// names — reads its history and appends to it. A run with no memory
	// still has an identity, but each one starts from nothing.
	ConversationMemory() bool

	// Offers decides whether a run of this scenario gets the named tool.
	// The default is everything; see ScheduledTaskScenario for the one
	// exception the runtime ships.
	Offers(toolName string) bool
}

// ScenarioBase is embeddable default behaviour: memory on, every tool.
type ScenarioBase struct{}

func (ScenarioBase) ConversationMemory() bool { return true }
func (ScenarioBase) Offers(string) bool       { return true }

// ChatScenario is the ordinary run: a user said something, the agent answers
// in the conversation.
var ChatScenario Scenario = &namedScenario{ScenarioBase{}, "CHAT"}

// ScheduledTaskScenario is a firing of a scheduled task. It does not get the
// schedule tools: a run that fires on a schedule must not be able to
// schedule more work — the firing prompt tells the model so, and Offers is
// what actually enforces it.
var ScheduledTaskScenario Scenario = &scheduledTaskScenario{namedScenario{ScenarioBase{}, "SCHEDULED_TASK"}}

// SubagentScenario is a run another run started. It joins no conversation —
// its whole task is the brief it was given, and a conversation of its own
// keeps it out of every store whatever the backend does. It gets neither the
// subagent tools (that is the depth cap: one level, enforced by name with no
// counter to get wrong) nor the schedule tools — unattended work must not
// leave work behind.
var SubagentScenario Scenario = &subagentScenario{namedScenario{ScenarioBase{}, "SUBAGENT"}}

type namedScenario struct {
	ScenarioBase
	name string
}

func (s *namedScenario) Name() string { return s.name }

type scheduledTaskScenario struct{ namedScenario }

func (s *scheduledTaskScenario) Offers(toolName string) bool {
	switch toolName {
	case tools.ToolNameCreateScheduledTask, tools.ToolNameListScheduledTasks, tools.ToolNameCancelScheduledTask:
		return false
	}
	return true
}

type subagentScenario struct{ namedScenario }

func (s *subagentScenario) ConversationMemory() bool { return false }

func (s *subagentScenario) Offers(toolName string) bool {
	switch toolName {
	case tools.ToolNameStartSubagent, tools.ToolNameWaitSubagent, tools.ToolNameCancelSubagent,
		tools.ToolNameCreateScheduledTask, tools.ToolNameListScheduledTasks, tools.ToolNameCancelScheduledTask:
		return false
	}
	return true
}

// ResponseListener observes a run. Every method has a safe default (a no-op
// or true), and every method is called with the run's failures contained: a
// callback that panics is recovered and logged, because one listener must
// not be able to fail another's run.
//
// OnContent receives the content so far, accumulated — not the deltas: a
// surface replaces what it shows, it does not append, so deltas would make
// every surface do its own accumulation and get it subtly wrong.
type ResponseListener interface {
	OnStart(run *RunContext)
	OnSubscribe()
	OnModel(model string)
	OnContent(contentSoFar string)
	// OnReasoning receives the reasoning so far, accumulated like OnContent
	// but reset per model call: a run's thinking is one block per model call,
	// joined by a blank line, because a second call after a tool result is a
	// new line of thought, not a continuation of the first.
	OnReasoning(reasoningSoFar string)
	OnUsage(model string, usage *schema.TokenUsage)
	// OnSubagent reports what a run this one started is doing. Only the
	// parent's listeners receive it, and only the fields the event kind
	// carries — see SubagentEvent.
	OnSubagent(event SubagentEvent)
	// OnMessageQueued reports that a message joined this run in flight via
	// FireOrQueue and is waiting to be read at the next tool boundary.
	// Display names the message (a preview, a sender); it is identifying
	// prose, not the body.
	OnMessageQueued(requestID, display string)
	// OnQueuedMessageRead reports the request ids the run just read into
	// itself at a tool boundary — messages this run answers, so their
	// senders are not waiting on a run of their own.
	OnQueuedMessageRead(requestIDs []string)
	OnError(err error)
	// OnFinished is called exactly once, whatever the run ended by.
	OnFinished(outcome Outcome)
	// ShouldContinue is polled between model iterations; false ends the run as
	// cancelled. It is how a surface that stopped listening (a closed card,
	// say) stops the work that was feeding it.
	ShouldContinue() bool
}

// SubagentEvent is one report about one subagent of the run. Which fields
// are set says what happened; the predicates name them. ContentSoFar is the
// subagent's accumulated answer (set while it is talking and once at the
// end); Usage is one model call's spend, attributed; Outcome is set when the
// subagent has ended.
type SubagentEvent struct {
	SubagentID   string
	Description  string
	ContentSoFar string
	Model        string
	Usage        *schema.TokenUsage
	Outcome      Outcome
}

// Started reports the subagent was started.
func (e SubagentEvent) Started() bool {
	return e.ContentSoFar == "" && e.Model == "" && e.Usage == nil && e.Outcome == ""
}

// Said reports ContentSoFar is set: the subagent's answer so far. A surface
// renders this where it shows work being waited on — never as the reply,
// which is what OnContent means.
func (e SubagentEvent) Said() bool { return e.ContentSoFar != "" && e.Outcome == "" }

// Spent reports Usage is set: tokens this subagent just used, counted on the
// parent's turn as well.
func (e SubagentEvent) Spent() bool {
	return e.Usage != nil && e.Outcome == ""
}

// Ended reports the subagent finished, and how; with the final ContentSoFar.
func (e SubagentEvent) Ended() bool { return e.Outcome != "" }

// ListenerFuncs adapts functions to ResponseListener. The zero value is a
// listener that observes nothing and always continues — embed it and fill
// the hooks a surface needs.
type ListenerFuncs struct {
	OnStartFunc     func(run *RunContext)
	OnSubscribeFunc func()
	OnModelFunc     func(model string)
	OnContentFunc   func(contentSoFar string)
	OnReasoningFunc func(reasoningSoFar string)
	OnUsageFunc     func(model string, usage *schema.TokenUsage)
	OnSubagentFunc  func(event SubagentEvent)
	// OnMessageQueuedFunc and OnQueuedMessageReadFunc observe FireOrQueue
	// joining messages to a run in flight.
	OnMessageQueuedFunc     func(requestID, display string)
	OnQueuedMessageReadFunc func(requestIDs []string)
	OnErrorFunc             func(err error)
	OnFinishedFunc          func(outcome Outcome)
	// ShouldContinueFunc nil means always continue.
	ShouldContinueFunc func() bool
}

// Compile-time check that the adapter satisfies the interface.
var _ ResponseListener = ListenerFuncs{}

func (l ListenerFuncs) OnStart(run *RunContext) {
	if l.OnStartFunc != nil {
		l.OnStartFunc(run)
	}
}

func (l ListenerFuncs) OnSubscribe() {
	if l.OnSubscribeFunc != nil {
		l.OnSubscribeFunc()
	}
}

func (l ListenerFuncs) OnModel(m string) {
	if l.OnModelFunc != nil {
		l.OnModelFunc(m)
	}
}

func (l ListenerFuncs) OnContent(c string) {
	if l.OnContentFunc != nil {
		l.OnContentFunc(c)
	}
}

func (l ListenerFuncs) OnReasoning(r string) {
	if l.OnReasoningFunc != nil {
		l.OnReasoningFunc(r)
	}
}

func (l ListenerFuncs) OnUsage(m string, u *schema.TokenUsage) {
	if l.OnUsageFunc != nil {
		l.OnUsageFunc(m, u)
	}
}

func (l ListenerFuncs) OnSubagent(e SubagentEvent) {
	if l.OnSubagentFunc != nil {
		l.OnSubagentFunc(e)
	}
}

func (l ListenerFuncs) OnMessageQueued(requestID, display string) {
	if l.OnMessageQueuedFunc != nil {
		l.OnMessageQueuedFunc(requestID, display)
	}
}

func (l ListenerFuncs) OnQueuedMessageRead(requestIDs []string) {
	if l.OnQueuedMessageReadFunc != nil {
		l.OnQueuedMessageReadFunc(requestIDs)
	}
}

func (l ListenerFuncs) OnError(err error) {
	if l.OnErrorFunc != nil {
		l.OnErrorFunc(err)
	}
}

func (l ListenerFuncs) OnFinished(o Outcome) {
	if l.OnFinishedFunc != nil {
		l.OnFinishedFunc(o)
	}
}

func (l ListenerFuncs) ShouldContinue() bool {
	if l.ShouldContinueFunc != nil {
		return l.ShouldContinueFunc()
	}
	return true
}

// RunContext is the per-run contribution point, handed to OnStart and valid
// for the duration of that call only: after OnStart returns, the agent reads
// it once and later mutations are dropped. A listener uses it to attach a
// late listener of its own, a todo handler, a question handler (the ask tool
// needs somewhere to put the questions before the run can offer it), or a
// context value tools will read.
type RunContext struct {
	request Request

	mu               sync.Mutex
	abortReason      string
	listeners        []ResponseListener
	todoHandlers     []tools.TodoEventHandler
	questionHandlers []tools.QuestionHandler
	// contextMutators decorate the run's context.Context, in order, before
	// any tool sees it.
	contextMutators []func(context.Context) context.Context
}

// Request returns the request this run context belongs to.
func (r *RunContext) Request() Request { return r.request }

// Abort stops the run before it starts, with a reason the error carries.
func (r *RunContext) Abort(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.abortReason = reason
}

// AbortReason is the reason passed to Abort, empty when none was.
func (r *RunContext) AbortReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abortReason
}

// AddListener attaches a listener to this run.
func (r *RunContext) AddListener(l ResponseListener) {
	if l == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, l)
}

// AddTodoHandler attaches a todo handler to this run.
func (r *RunContext) AddTodoHandler(h tools.TodoEventHandler) {
	if h == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.todoHandlers = append(r.todoHandlers, h)
}

// AddQuestionHandler attaches a question handler to this run. Dropped for
// background runs, where there is no surface to put a question on and no
// card to answer it — an ask that cannot be presented fails the ask, which
// the model experiences as "no channel could ask".
func (r *RunContext) AddQuestionHandler(h tools.QuestionHandler) {
	if h == nil || r.request.Background {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.questionHandlers = append(r.questionHandlers, h)
}

// AddContext adds a context decorator the run's tool context passes
// through.
func (r *RunContext) AddContext(mutate func(context.Context) context.Context) {
	if mutate == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contextMutators = append(r.contextMutators, mutate)
}

// The package-private accessors below are read once by the agent, after
// OnStart.

func (r *RunContext) listenerSnapshot() []ResponseListener {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ResponseListener(nil), r.listeners...)
}

func (r *RunContext) todoHandlerSnapshot() []tools.TodoEventHandler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tools.TodoEventHandler(nil), r.todoHandlers...)
}

func (r *RunContext) questionHandlerSnapshot() []tools.QuestionHandler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tools.QuestionHandler(nil), r.questionHandlers...)
}

func (r *RunContext) contextMutatorSnapshot() []func(context.Context) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]func(context.Context) context.Context(nil), r.contextMutators...)
}

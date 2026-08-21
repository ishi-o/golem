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

// ResponseListener observes a run. Every method has a safe default (a no-op
// or true), and every method is called with the run's failures contained: a
// callback that panics is recovered and logged, because one listener must
// not be able to fail another's run.
//
// OnContent receives the content so far, accumulated — not the deltas: a
// surface replaces what it shows, it does not append, so deltas would make
// every surface do its own accumulation and get it subtly wrong.
type ResponseListener interface {
	OnStart(registry *RunRegistry)
	OnSubscribe()
	OnModel(model string)
	OnContent(contentSoFar string)
	OnUsage(model string, usage *schema.TokenUsage)
	OnError(err error)
	// OnFinished is called exactly once, whatever the run ended by.
	OnFinished(outcome Outcome)
	// ShouldContinue is polled between chunks; false ends the run as
	// cancelled. It is how a surface that stopped listening (a closed card,
	// say) stops the work that was feeding it.
	ShouldContinue() bool
}

// ListenerFuncs adapts functions to ResponseListener. The zero value is a
// listener that observes nothing and always continues — embed it and fill
// the hooks a surface needs.
type ListenerFuncs struct {
	OnStartF     func(registry *RunRegistry)
	OnSubscribeF func()
	OnModelF     func(model string)
	OnContentF   func(contentSoFar string)
	OnUsageF     func(model string, usage *schema.TokenUsage)
	OnErrorF     func(err error)
	OnFinishedF  func(outcome Outcome)
	// ShouldContinueF nil means always continue.
	ShouldContinueF func() bool
}

// Compile-time check that the adapter satisfies the interface.
var _ ResponseListener = ListenerFuncs{}

func (l ListenerFuncs) OnStart(r *RunRegistry) {
	if l.OnStartF != nil {
		l.OnStartF(r)
	}
}

func (l ListenerFuncs) OnSubscribe() {
	if l.OnSubscribeF != nil {
		l.OnSubscribeF()
	}
}

func (l ListenerFuncs) OnModel(m string) {
	if l.OnModelF != nil {
		l.OnModelF(m)
	}
}

func (l ListenerFuncs) OnContent(c string) {
	if l.OnContentF != nil {
		l.OnContentF(c)
	}
}

func (l ListenerFuncs) OnUsage(m string, u *schema.TokenUsage) {
	if l.OnUsageF != nil {
		l.OnUsageF(m, u)
	}
}

func (l ListenerFuncs) OnError(err error) {
	if l.OnErrorF != nil {
		l.OnErrorF(err)
	}
}

func (l ListenerFuncs) OnFinished(o Outcome) {
	if l.OnFinishedF != nil {
		l.OnFinishedF(o)
	}
}

func (l ListenerFuncs) ShouldContinue() bool {
	if l.ShouldContinueF != nil {
		return l.ShouldContinueF()
	}
	return true
}

// RunRegistry is the per-run contribution point, handed to OnStart and valid
// for the duration of that call only: after OnStart returns, the agent reads
// it once and later mutations are dropped. A listener uses it to attach a
// late listener of its own, a todo handler, a question handler (the ask tool
// needs somewhere to put the questions before the run can offer it), or a
// context value tools will read.
type RunRegistry struct {
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

// Request returns the run the registry belongs to.
func (r *RunRegistry) Request() Request { return r.request }

// Abort stops the run before it starts, with a reason the error carries.
func (r *RunRegistry) Abort(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.abortReason = reason
}

// AbortReason is the reason passed to Abort, empty when none was.
func (r *RunRegistry) AbortReason() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.abortReason
}

// AddResponseListener attaches a listener to this run.
func (r *RunRegistry) AddResponseListener(l ResponseListener) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.listeners = append(r.listeners, l)
}

// AddTodoHandler attaches a todo handler to this run.
func (r *RunRegistry) AddTodoHandler(h tools.TodoEventHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.todoHandlers = append(r.todoHandlers, h)
}

// AddQuestionHandler attaches a question handler to this run. Dropped for
// background runs, where there is no surface to put a question on and no
// card to answer it — an ask that cannot be presented fails the ask, which
// the model experiences as "no channel could ask".
func (r *RunRegistry) AddQuestionHandler(h tools.QuestionHandler) {
	if r.request.Background {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.questionHandlers = append(r.questionHandlers, h)
}

// AddContext adds a context decorator the run's tool context passes
// through.
func (r *RunRegistry) AddContext(mutate func(context.Context) context.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.contextMutators = append(r.contextMutators, mutate)
}

// The package-private accessors below are read once by the agent, after
// OnStart.

func (r *RunRegistry) extraListeners() []ResponseListener {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ResponseListener(nil), r.listeners...)
}

func (r *RunRegistry) todoEventHandlers() []tools.TodoEventHandler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tools.TodoEventHandler(nil), r.todoHandlers...)
}

func (r *RunRegistry) questionHandlersList() []tools.QuestionHandler {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]tools.QuestionHandler(nil), r.questionHandlers...)
}

func (r *RunRegistry) contextDecorators() []func(context.Context) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]func(context.Context) context.Context(nil), r.contextMutators...)
}

package agent

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/chatmemory"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/i18n"
	"github.com/ishi-o/golem/core/knowledge"
	"github.com/ishi-o/golem/core/prompt"
	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/tools"
)

// Agent is the runtime entry point. Fire starts a run and Cancel stops one;
// models, memory, tools, and observers are supplied by the embedding
// application.
//
// The loop is explicit instead of using a higher-level agent runner so this
// package owns the runtime semantics: accumulated content callbacks,
// cancellation, tool interception, turn-ending tools, and message store.
type Agent struct {
	model    model.ToolCallingChatModel
	memory   chatmemory.Repository
	provider *tools.Provider
	// backend supplies the pending-question repo the outstanding-ask guard
	// reads. Nil skips the guard (a harness with no persistence), documented
	// on asking().
	backend store.Backend
	config  config.Config
	// messages localizes the model-facing instruction strings (the ask tool's
	// recorded result, above all).
	messages *i18n.Bundle
	// memoryWindow bounds how much history a run loads; 0 loads all of it.
	memoryWindow int
	// modelName names the model in OnModel/OnUsage callbacks when the stream
	// does not carry one.
	modelName string
	// knowledge is optional. It is a facade over Eino-native retrievers and is
	// consulted before each ordinary model call's user message is built.
	knowledge       knowledge.KnowledgeBase
	knowledgeConfig knowledge.RetrievalConfig
	// defaultListeners observe every run, however it was started — how a
	// surface takes part in runs it did not initiate (a scheduled task
	// firing, say).
	defaultListeners []ResponseListener
	log              *slog.Logger

	accepting atomic.Bool
	inFlight  atomic.Int64
	mu        sync.Mutex
	liveRuns  map[string]*liveRun
}

// liveRun is one run in flight: what Cancel needs, the listeners a
// subagent's progress is reported to, and the children a run must outlive.
type liveRun struct {
	cancel  context.CancelFunc
	request Request
	// cancelled is set by Cancel; a child registered after its parent was
	// cancelled reads it at its loop head, because there is no context to
	// inherit through Fire.
	cancelled atomic.Bool
	// listeners is assigned once, after the run's OnStart additions have
	// joined; subagents only start from tool calls, which happen after that.
	listeners []ResponseListener
	// children maps each subagent's request id to a channel closed when that
	// subagent has finished. The parent waits on these before reporting
	// itself finished, so a subagent nobody collected is still paid for and
	// still reported.
	children map[string]chan struct{}
	// queued is where a reply that arrives while the run is in flight joins
	// it; see FireOrQueue.
	queued *queuedMessages
}

// AgentOption configures an Agent during construction.
type AgentOption func(*Agent)

// WithBackend supplies the persistence backend used by the pending-question
// guard. The agent does not own the backend or any of its connections.
func WithBackend(backend store.Backend) AgentOption {
	return func(a *Agent) { a.backend = backend }
}

// WithLogger supplies the logger used for runtime and callback failures.
func WithLogger(log *slog.Logger) AgentOption {
	return func(a *Agent) {
		if log != nil {
			a.log = log
		}
	}
}

// WithMemoryWindow limits the number of conversation messages loaded per run.
// A non-positive value keeps the full conversation.
func WithMemoryWindow(window int) AgentOption {
	return func(a *Agent) {
		if window > 0 {
			a.memoryWindow = window
		}
	}
}

// WithModelName supplies a fallback name for model callbacks when the stream
// does not include one.
func WithModelName(name string) AgentOption {
	return func(a *Agent) { a.modelName = name }
}

// WithKnowledgeBase attaches an optional scoped knowledge base. Retrieval is
// best-effort: a vector-store outage is logged and the conversation still has
// a chance to answer from its normal context.
func WithKnowledgeBase(base knowledge.KnowledgeBase, cfg knowledge.RetrievalConfig) AgentOption {
	return func(a *Agent) {
		a.knowledge = base
		a.knowledgeConfig = cfg
	}
}

// WithDefaultListener adds a listener that observes every run.
func WithDefaultListener(listener ResponseListener) AgentOption {
	return func(a *Agent) {
		if listener != nil {
			a.defaultListeners = append(a.defaultListeners, listener)
		}
	}
}

// AddDefaultListener adds a listener that observes every run, for observers
// that only come into existence after the agent was constructed — the
// subagent tools, which learn at Fire time which run's registry to forget.
// Call before the first Fire: a race against a starting run means that run
// may or may not carry the listener, never a corruption.
func (a *Agent) AddDefaultListener(l ResponseListener) {
	if l == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.defaultListeners = append(a.defaultListeners, l)
}

// New constructs an Agent and starts accepting runs.
func New(m model.ToolCallingChatModel, mem chatmemory.Repository, provider *tools.Provider, cfg config.Config, options ...AgentOption) *Agent {
	_ = cfg.Normalize()
	a := &Agent{model: m, memory: mem, provider: provider, config: cfg, log: slog.Default(), liveRuns: map[string]*liveRun{}}
	for _, option := range options {
		if option != nil {
			option(a)
		}
	}
	a.messages = i18n.New(cfg.Locale, a.log)
	a.accepting.Store(true)
	return a
}

// Accepting reports whether Fire takes runs. False after Shutdown begins.
func (a *Agent) Accepting() bool { return a.accepting.Load() }

// Fire starts a run and returns immediately; results reach listeners, never
// the caller. A caller that has to wait for the answer waits on a listener.
func (a *Agent) Fire(req Request) error {
	if !a.accepting.Load() {
		return fmt.Errorf("golem: not accepting runs (shutting down)")
	}
	if req.Scenario == nil {
		return fmt.Errorf("golem: request has no scenario")
	}
	ready := make(chan struct{})
	go a.run(req, ready)
	// Fire remains non-blocking with respect to model and tool work, but it
	// does not return before a request id can be cancelled. Without this
	// small handshake a caller could receive nil and immediately lose a
	// Cancel race against the new run's goroutine.
	<-ready
	return nil
}

// Cancel stops a run by request id. False when no such run is live — already
// finished, or never started. The cancellation goes down the tree as well:
// a subagent nobody is waiting for any more is still work with a shell or an
// MCP call in it, and cancelling the parent alone would leave it running.
func (a *Agent) Cancel(requestID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if run, ok := a.liveRuns[requestID]; ok {
		a.cancelTreeLocked(requestID, run)
		return true
	}
	return false
}

// FireOrQueue offers a message to the run already in flight over the same
// conversation, by the same user — a correction, an addition, an answer to
// "should I go on?" arriving while the agent works. True means the run took
// it: it is read into the turn at the next tool boundary, and this message
// needs no run of its own. False means nobody matching is live (or the run
// had already stopped reading), and the caller should Fire the request —
// which is why this does not fire on the caller's behalf: the caller built
// the request and owns what a refusal of it means.
//
// A run matches when it is of the same conversation and the same user, not
// a background run and not a subagent: a group card being public does not
// make someone else's aside a correction of this run, and a subagent has no
// conversation to correct.
func (a *Agent) FireOrQueue(req Request, text func() string, display string) bool {
	a.mu.Lock()
	var target *liveRun
	for _, run := range a.liveRuns {
		if req.ConversationID != "" &&
			run.request.ConversationID == req.ConversationID &&
			run.request.UserID == req.UserID &&
			!run.request.Background &&
			run.request.ParentRequestID == "" {
			target = run
			break
		}
	}
	if target == nil {
		a.mu.Unlock()
		return false
	}
	queued := target.queued
	listeners := target.listeners
	a.mu.Unlock()
	// Offered outside the agent lock: the queue is closed under its own
	// lock, so a run that ends between the two calls makes offer return
	// false, and the caller falls back to firing — never a lost message.
	if !queued.offer(req, text, display) {
		return false
	}
	for _, l := range listeners {
		safeNotify(a, "OnMessageQueued", l, func(li ResponseListener) { li.OnMessageQueued(req.RequestID, display) })
	}
	return true
}

func (a *Agent) cancelTreeLocked(requestID string, run *liveRun) {
	run.cancelled.Store(true)
	run.cancel()
	for childID := range run.children {
		if child, live := a.liveRuns[childID]; live {
			a.cancelTreeLocked(childID, child)
		}
	}
}

// notifySubagent reports a subagent event to the listeners of the run that
// started it.
func notifySubagent(a *Agent, parent *liveRun, event SubagentEvent) {
	for _, l := range parent.listeners {
		safeNotify(a, "OnSubagent", l, func(li ResponseListener) { li.OnSubagent(event) })
	}
}

// waitProgressInterval is how often a run waiting on its subagents says so:
// silence for half an hour is indistinguishable from the hang this replaced.
const waitProgressInterval = 30 * time.Second

// awaitChildren waits for every run this one started that has not ended. A
// subagent is work the turn asked for, and abandoning it halfway would leave
// a shell command or an MCP call running with nothing watching it — so this
// waits, and generously. But not for ever: past the ceiling this is a fault
// rather than slow work, so it is said out loud and the run stops being held
// for it. Runs on the run's own goroutine, so plain blocking is safe.
func (a *Agent) awaitChildren(requestID string, live *liveRun) {
	if len(live.children) == 0 {
		return
	}
	timeout := a.config.AI.Tools.Subagent.WaitTimeout
	a.log.Info("waiting for subagents to finish", "run", requestID, "count", len(live.children), "timeout", timeout)
	deadline := time.Now().Add(timeout)
	for childID, done := range live.children {
		for {
			remaining := time.Until(deadline)
			if remaining <= 0 {
				a.log.Error("subagent did not finish in time; cancelling it", "run", requestID, "subagent", childID, "timeout", timeout)
				a.Cancel(childID)
				break
			}
			slice := remaining
			if slice > waitProgressInterval {
				slice = waitProgressInterval
			}
			select {
			case <-done:
			case <-time.After(slice):
				a.log.Info("still waiting for subagent", "run", requestID, "subagent", childID)
				continue
			}
			break
		}
	}
}

// Shutdown stops accepting and waits for in-flight runs, up to ten minutes —
// long, deliberately, because a run may be mid-tool-call on a slow MCP
// server, and killing it loses the turn a user is watching a card for. The
// caller decides what happens when the wait gives out.
func (a *Agent) Shutdown(ctx context.Context) error {
	a.accepting.Store(false)
	deadline := time.Now().Add(10 * time.Minute)
	for a.inFlight.Load() > 0 && time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
			a.log.Info("waiting for in-flight runs", "count", a.inFlight.Load())
		}
	}
	if a.inFlight.Load() > 0 {
		return fmt.Errorf("golem: %d runs still in flight after shutdown wait", a.inFlight.Load())
	}
	return nil
}

type runState struct {
	a         *Agent
	listeners []ResponseListener
}

func safeNotify(a *Agent, name string, l ResponseListener, call func(l ResponseListener)) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("listener callback panicked", "callback", name, "panic", r)
		}
	}()
	call(l)
}

func (s *runState) notifyAll(name string, call func(l ResponseListener)) {
	for _, l := range s.listeners {
		safeNotify(s.a, name, l, call)
	}
}

func (s *runState) shouldContinue() bool {
	for _, l := range s.listeners {
		if !safeShouldContinue(s.a, l) {
			return false
		}
	}
	return true
}

func safeShouldContinue(a *Agent, l ResponseListener) (ok bool) {
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("listener shouldContinue panicked", "panic", r)
			// A listener that cannot be asked cannot veto the run either.
			ok = true
		}
	}()
	return l.ShouldContinue()
}

// run executes one run on its own goroutine. Every exit path flows through
// the deferred finish so OnFinished fires exactly once and the composition's
// MCP connections close whatever the run ended by.
func (a *Agent) run(req Request, ready chan<- struct{}) {
	a.inFlight.Add(1)
	defer a.inFlight.Add(-1)

	// Cancellation: the cancel flag doubles as the outcome's verdict — a run
	// stopped by Cancel ends CANCELLED, not FAILED.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	live := &liveRun{cancel: cancel, request: req, children: map[string]chan struct{}{}}
	live.queued = &queuedMessages{onRead: func(ids []string) {
		for _, l := range live.listeners {
			safeNotify(a, "OnQueuedMessageRead", l, func(li ResponseListener) { li.OnQueuedMessageRead(ids) })
		}
	}}
	if req.RequestID != "" {
		a.mu.Lock()
		a.liveRuns[req.RequestID] = live
		a.mu.Unlock()
		// Removed at the end of finish, and not before: while the run is
		// waiting out its subagents it must stay cancellable — that wait is
		// exactly when cancelling it is what would release it.
		defer func() {
			a.mu.Lock()
			delete(a.liveRuns, req.RequestID)
			a.mu.Unlock()
		}()
	}

	// The run that started this one, if it is still going. A parent that has
	// already ended is left out of everything below: there is nobody there to
	// tell, and nobody waiting.
	var parent *liveRun
	var doneForParent chan struct{}
	if req.ParentRequestID != "" && req.RequestID != "" {
		a.mu.Lock()
		if p, ok := a.liveRuns[req.ParentRequestID]; ok {
			parent = p
			doneForParent = make(chan struct{})
			parent.children[req.RequestID] = doneForParent
		}
		a.mu.Unlock()
		if parent != nil {
			defer func() {
				// Last of all: the parent is held open on this channel, so
				// anything that skipped closing it would hold that run open
				// until the application shut down.
				a.mu.Lock()
				delete(parent.children, req.RequestID)
				a.mu.Unlock()
				close(doneForParent)
			}()
		}
	}
	close(ready)

	runContext := &RunContext{request: req}
	a.mu.Lock()
	defaults := append([]ResponseListener(nil), a.defaultListeners...)
	a.mu.Unlock()
	state := &runState{a: a, listeners: append(defaults, req.Listeners...)}

	cancelled := false
	var composition *tools.Composition
	// content accumulates the run's answer; it outlives the loop because the
	// parent's end-of-subagent report carries the final answer.
	content := &strings.Builder{}

	finish := func(outcome Outcome, err error) {
		// First of all: the model has stopped, so nothing can be read into
		// this run any more, and a message arriving from here on has to be
		// answered some other way. Before the wait below, which is measured
		// in minutes the queue would be dead for.
		unread := live.queued.close()
		if err != nil {
			state.notifyAll("OnError", func(l ResponseListener) { l.OnError(err) })
		}
		// Before this run reports itself finished: the subagents it started
		// are still going, and they belong to it — an answer nobody collected
		// is still paid for and still attributable.
		a.awaitChildren(req.RequestID, live)
		state.notifyAll("OnFinished", func(l ResponseListener) { l.OnFinished(outcome) })
		if parent != nil {
			notifySubagent(a, parent, SubagentEvent{
				SubagentID: req.RequestID, Description: req.Description,
				ContentSoFar: content.String(), Outcome: outcome,
			})
		}
		if composition != nil && composition.Close != nil {
			composition.Close()
		}
		// Last, once this run has let go of everything it held: whatever the
		// user said that the run never got round to reading is a message
		// nobody has answered, so it is answered now, as the run it would
		// have been. After OnFinished above rather than before, so the
		// answer it starts appears under one that has already been finalized.
		for _, late := range unread {
			if err := a.Fire(late); err != nil {
				a.log.Error("re-firing a message the run never read failed", "run", late.RequestID, "err", err)
			}
		}
	}
	if a.model == nil {
		finish(OutcomeFailed, fmt.Errorf("golem: no chat model configured"))
		return
	}

	// onStart is where the run context fills; extra listeners join before any
	// other callback fires.
	state.notifyAll("OnStart", func(l ResponseListener) { l.OnStart(runContext) })
	state.listeners = append(state.listeners, runContext.listenerSnapshot()...)
	live.listeners = state.listeners
	if parent != nil {
		notifySubagent(a, parent, SubagentEvent{SubagentID: req.RequestID, Description: req.Description})
	}

	if reason := runContext.AbortReason(); reason != "" {
		finish(OutcomeFailed, fmt.Errorf("run aborted before it started: %s", reason))
		return
	}

	runCtx := req.context(ctx, runContext.contextMutatorSnapshot())

	// The ask machinery: one handler fanned out, or none at all.
	questionHandlers := runContext.questionHandlerSnapshot()
	var questionHandler tools.QuestionHandler
	answersArriveLater := true
	if len(questionHandlers) > 0 {
		questionHandler = a.fanOutQuestions(runCtx, questionHandlers)
		for _, h := range questionHandlers {
			if inline, ok := h.(tools.InlineAnswers); ok && inline.AnswersInline() {
				answersArriveLater = false
				break
			}
		}
	}

	todoFan := tools.TodoFanOut(append(append([]tools.TodoEventHandler(nil), req.TodoHandlers...), runContext.todoHandlerSnapshot()...))

	if a.provider == nil {
		finish(OutcomeFailed, fmt.Errorf("golem: no tools provider configured"))
		return
	}
	composition, err := a.provider.Compose(runCtx, tools.ComposeRequest{
		ScenarioOffers:     req.Scenario.Offers,
		UserID:             req.UserID,
		ChatID:             req.ChatID,
		GroupID:            req.GroupID,
		TenantID:           req.TenantID,
		TodoHandler:        todoFan,
		Questions:          a.asking(runCtx, req, questionHandler),
		AnswersArriveLater: answersArriveLater,
		AskedMessage:       a.message(i18n.QuestionAsked),
		AskEnabled:         a.config.AI.Tools.AskUserQuestion.Enabled,
	})
	if err != nil {
		finish(OutcomeFailed, fmt.Errorf("composing tools: %w", err))
		return
	}
	modelWithTools, err := a.model.WithTools(composition.Info)
	if err != nil {
		finish(OutcomeFailed, fmt.Errorf("binding tools: %w", err))
		return
	}

	state.notifyAll("OnSubscribe", func(l ResponseListener) { l.OnSubscribe() })

	messages, err := a.buildMessages(runCtx, req, composition)
	if err != nil {
		finish(OutcomeFailed, err)
		return
	}

	// Persist this run's new messages at the end: the user message, the
	// assistant/tool turns, whatever the outcome — a cancelled run's partial
	// work is history too, and the next run continuing over it is better
	// than it replaying the same question.
	var newMessages []*schema.Message
	userMsg := req.UserMessage()
	messages = append(messages, userMsg)
	newMessages = append(newMessages, userMsg)

	var modelOnce sync.Once
	modelName := ""
	reportContent := func(soFar string) {
		state.notifyAll("OnContent", func(l ResponseListener) { l.OnContent(soFar) })
		// Up to the parent as its own kind of event, never as content: a
		// surface renders content as the reply, so handing it the
		// subagent's would overwrite the parent's answer.
		if parent != nil {
			notifySubagent(a, parent, SubagentEvent{
				SubagentID: req.RequestID, Description: req.Description, ContentSoFar: soFar,
			})
		}
	}
	// Reasoning accumulates like content but per model call: each call's
	// thinking is one block, and the callback carries the blocks so far with
	// the current call's at the end.
	var reasoningBlocks []string
	reportReasoning := func(call string) {
		soFar := strings.Join(append(append([]string(nil), reasoningBlocks...), call), "\n\n")
		state.notifyAll("OnReasoning", func(l ResponseListener) { l.OnReasoning(soFar) })
	}

	for iteration := 0; ; iteration++ {
		if ctx.Err() != nil || (parent != nil && parent.cancelled.Load()) || !state.shouldContinue() {
			cancelled = true
			break
		}
		callReasoning := &strings.Builder{}
		reader, err := modelWithTools.Stream(runCtx, messages)
		if err != nil {
			if runCtx.Err() != nil {
				finish(OutcomeCancelled, nil)
				return
			}
			finish(OutcomeFailed, fmt.Errorf("model stream: %w", err))
			return
		}
		var chunks []*schema.Message
		for {
			chunk, err := reader.Recv()
			if err == io.EOF {
				break
			}
			if err != nil {
				reader.Close()
				if runCtx.Err() != nil {
					finish(OutcomeCancelled, nil)
					return
				}
				finish(OutcomeFailed, fmt.Errorf("model stream: %w", err))
				return
			}
			if chunk == nil {
				continue
			}
			chunks = append(chunks, chunk)
			if chunk.Content != "" {
				content.WriteString(chunk.Content)
				reportContent(content.String())
			}
			if chunk.ReasoningContent != "" {
				// Some providers stream the whole call's thinking so far on
				// every chunk rather than the delta; a chunk that already
				// contains what was accumulated is one of those, so it
				// replaces the buffer instead of appending to it.
				if soFar := callReasoning.String(); soFar != "" && strings.HasPrefix(chunk.ReasoningContent, soFar) {
					callReasoning.Reset()
				}
				callReasoning.WriteString(chunk.ReasoningContent)
				reportReasoning(callReasoning.String())
			}
			if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil && chunk.ResponseMeta.Usage.TotalTokens > 0 {
				usage := chunk.ResponseMeta.Usage
				name := modelName
				modelOnce.Do(func() {
					if name == "" {
						name = a.modelName
					}
					modelName = name
					finalName := name
					state.notifyAll("OnModel", func(l ResponseListener) { l.OnModel(finalName) })
				})
				state.notifyAll("OnUsage", func(l ResponseListener) { l.OnUsage(modelName, usage) })
				// Tokens a subagent spends are spent on the parent's turn,
				// so they belong in the count the parent shows — as usage
				// for the turn's total, and again as a subagent event so a
				// surface showing each subagent separately knows whose spend
				// this was.
				if parent != nil {
					for _, l := range parent.listeners {
						safeNotify(a, "OnUsage", l, func(li ResponseListener) { li.OnUsage(modelName, usage) })
					}
					notifySubagent(a, parent, SubagentEvent{
						SubagentID: req.RequestID, Description: req.Description,
						Model: modelName, Usage: usage,
					})
				}
			}
		}
		reader.Close()
		// The call's thinking is done; the next model call, if any, starts a
		// new block.
		if block := callReasoning.String(); block != "" {
			reasoningBlocks = append(reasoningBlocks, block)
		}
		if len(chunks) == 0 {
			break
		}
		assistant, err := schema.ConcatMessages(chunks)
		if err != nil {
			finish(OutcomeFailed, fmt.Errorf("concatenating stream chunks: %w", err))
			return
		}
		messages = append(messages, assistant)
		newMessages = append(newMessages, assistant)

		if len(assistant.ToolCalls) == 0 {
			break
		}
		// Execute the tool calls, append their results, loop. A tool error
		// is a tool result the model reads, not a run failure — the model
		// gets to correct itself.
		ended := false
		for _, call := range assistant.ToolCalls {
			result, endTurn, err := a.executeTool(runCtx, composition, call)
			if err != nil {
				if runCtx.Err() != nil {
					finish(OutcomeCancelled, nil)
					return
				}
				result, endTurn = fmt.Sprintf("tool error: %v", err), false
			}
			toolMsg := &schema.Message{Role: schema.Tool, ToolCallID: call.ID, ToolName: call.Function.Name, Content: result}
			messages = append(messages, toolMsg)
			newMessages = append(newMessages, toolMsg)
			if endTurn {
				ended = true
			}
		}
		if ended {
			// A turn-ending tool (the ask, with answers arriving later):
			// the result is for the application and the next run, not for
			// the model to continue from.
			break
		}
		// A reply that arrived while the turn was working joins it here, at
		// the tool boundary: all tool calls of the iteration are answered
		// and nothing is outstanding — the one safe point in the loop.
		for _, text := range live.queued.read() {
			late := &schema.Message{Role: schema.User, Content: a.message(i18n.QueuedMessage, text)}
			messages = append(messages, late)
			newMessages = append(newMessages, late)
		}
	}

	if a.memory != nil && req.Scenario.ConversationMemory() && req.ConversationID != "" {
		if err := a.memory.Append(runCtx, req.ConversationID, newMessages); err != nil {
			a.log.Error("appending chat memory", "conversation", req.ConversationID, "err", err)
		}
	}

	if cancelled || ctx.Err() != nil {
		finish(OutcomeCancelled, nil)
		return
	}
	finish(OutcomeCompleted, nil)
}

// executeTool dispatches one tool call through the composition's wrapped
// tools, unwrapping the end-turn sentinel (see interceptor.go).
func (a *Agent) executeTool(ctx context.Context, comp *tools.Composition, call schema.ToolCall) (string, bool, error) {
	for _, t := range comp.Tools {
		info, err := t.Info(ctx)
		if err != nil || info == nil || info.Name != call.Function.Name {
			continue
		}
		result, err := t.InvokableRun(ctx, call.Function.Arguments)
		if err != nil {
			return "", false, err
		}
		result, end := tools.SplitEndTurn(result)
		return result, end, nil
	}
	// An unroutable tool call is a recovery instruction, not a failure: the
	// model hallucinating a tool it was not offered is a normal outcome of
	// an index-based tool set.
	return fmt.Sprintf("no tool named %q is available in this run; say so and continue without it", call.Function.Name), false, nil
}

// buildMessages renders the system prompt and loads history.
func (a *Agent) buildMessages(ctx context.Context, req Request, comp *tools.Composition) ([]*schema.Message, error) {
	vars := map[string]string{
		"threadId": "",
		"parentId": "",
		"mentions": "none",
	}
	for k, v := range req.PromptVariables {
		vars[k] = v
	}
	vars["userId"] = req.UserID
	vars["chatId"] = req.ChatID
	vars["chatType"] = req.ChatType
	system, err := prompt.Render(a.config.AI.SystemPrompt, vars)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}
	messages := []*schema.Message{{Role: schema.System, Content: system}}
	if a.knowledge != nil {
		retrieval := knowledge.KnowledgeRetrieval{Scope: knowledge.NewScope(req.UserID, req.GroupID, req.TenantID), Query: req.Text, TopK: a.knowledgeConfig.TopK}
		if req.KnowledgeRetrieval != nil {
			retrieval = *req.KnowledgeRetrieval
			if strings.TrimSpace(retrieval.Query) == "" {
				retrieval.Query = req.Text
			}
		}
		topK := retrieval.TopK
		if topK <= 0 {
			topK = a.knowledgeConfig.TopK
		}
		if topK <= 0 {
			topK = 4
		}
		if strings.TrimSpace(retrieval.Query) != "" {
			var documents []*schema.Document
			var searchErr error
			filteredSearch, supportsFiltering := a.knowledge.(knowledge.FilteredKnowledgeBase)
			if retrieval.Filter != nil && supportsFiltering {
				documents, searchErr = filteredSearch.SearchFiltered(ctx, retrieval.Scope, retrieval.Query, topK, retrieval.Filter)
			} else {
				documents, searchErr = a.knowledge.Search(ctx, retrieval.Scope, retrieval.Query, topK)
			}
			if searchErr != nil {
				a.log.Warn("knowledge retrieval failed; continuing without it", "err", searchErr)
			} else {
				if retrieval.Filter != nil {
					filtered := documents[:0]
					for _, document := range documents {
						if document != nil && retrieval.Filter(knowledge.ReadMetadata(document.MetaData)) {
							filtered = append(filtered, document)
						}
					}
					documents = filtered
				}
				if len(documents) > 0 {
					messages = append(messages, &schema.Message{Role: schema.System, Content: knowledge.ContextText(documents, a.knowledgeConfig.MaxChars)})
				}
			}
		}
	}
	if a.memory != nil && req.Scenario.ConversationMemory() && req.ConversationID != "" {
		history, err := a.memory.Load(ctx, req.ConversationID, a.memoryWindow)
		if err != nil {
			return nil, fmt.Errorf("loading chat memory: %w", err)
		}
		messages = append(messages, history...)
	}
	return messages, nil
}

// message resolves a localized runtime string.
func (a *Agent) message(key string, args ...any) string {
	if a.messages != nil {
		return a.messages.Get(key, args...)
	}
	return key
}

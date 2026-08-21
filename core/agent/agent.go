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
	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/i18n"
	"github.com/ishi-o/golem/core/prompt"
	"github.com/ishi-o/golem/core/tools"
)

// Agent is the one entry point for running the agent — the Go shape of
// spring-agent's SpringAgent. Fire and Cancel are the whole public surface;
// everything else is wiring fields.
//
// The run loop is hand-rolled over eino's ToolCallingChatModel rather than eino's
// canned react agent, for the same reason SpringAgent drives a ChatClient
// instead of Spring AI's ChatAgent: the runtime's semantics — accumulated
// content callbacks, cancellation between chunks, the interceptor chain, the
// ask tool's turn-ending sentinel, tool messages persisted to memory — are
// all things a canned loop either hides or re-decides.
type Agent struct {
	Model    model.ToolCallingChatModel
	Memory   chatmemory.Repository
	Provider *tools.Provider
	// Repos supplies the pending-question repo the outstanding-ask guard
	// reads. Nil skips the guard (a harness with no persistence), documented
	// on asking().
	Repos  dao.Backend
	Config config.Config
	// Messages localizes the model-facing instruction strings (the ask
	// tool's recorded result, above all).
	Messages *i18n.Bundle
	// MemoryWindow bounds how much history a run loads; 0 loads all of it.
	MemoryWindow int
	// ModelName names the model in OnModel/OnUsage callbacks when the stream
	// does not carry one.
	ModelName string
	// DeclaredListeners observe every run, however it was started — how a
	// surface takes part in runs it did not initiate (a scheduled task
	// firing, say).
	DeclaredListeners []ResponseListener
	Log               *slog.Logger

	accepting atomic.Bool
	inFlight  atomic.Int64
	mu        sync.Mutex
	cancels   map[string]context.CancelFunc
}

// New wires an Agent and starts accepting runs.
func New(m model.ToolCallingChatModel, mem chatmemory.Repository, provider *tools.Provider, cfg config.Config) *Agent {
	_ = cfg.Normalize()
	a := &Agent{Model: m, Memory: mem, Provider: provider, Config: cfg, Log: slog.Default(), cancels: map[string]context.CancelFunc{}}
	a.Messages = i18n.New(cfg.Locale, a.Log)
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
// finished, or never started.
func (a *Agent) Cancel(requestID string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	cancel, ok := a.cancels[requestID]
	if ok {
		cancel()
	}
	return ok
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
			a.Log.Info("waiting for in-flight runs", "count", a.inFlight.Load())
		}
	}
	if a.inFlight.Load() > 0 {
		return fmt.Errorf("golem: %d runs still in flight after shutdown wait", a.inFlight.Load())
	}
	return nil
}

// notify calls a listener callback with panics contained.
func notify(a *Agent, name string, call func(l ResponseListener)) {
	for _, l := range append([]ResponseListener(nil), a.DeclaredListeners...) {
		safeNotify(a, name, l, call)
	}
}

type runState struct {
	a         *Agent
	listeners []ResponseListener
}

func safeNotify(a *Agent, name string, l ResponseListener, call func(l ResponseListener)) {
	defer func() {
		if r := recover(); r != nil {
			a.Log.Error("listener callback panicked", "callback", name, "panic", r)
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
			a.Log.Error("listener shouldContinue panicked", "panic", r)
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
	if req.RequestID != "" {
		a.mu.Lock()
		a.cancels[req.RequestID] = cancel
		a.mu.Unlock()
		defer func() {
			a.mu.Lock()
			delete(a.cancels, req.RequestID)
			a.mu.Unlock()
		}()
	}
	close(ready)

	registry := &RunRegistry{request: req}
	state := &runState{a: a, listeners: append(append([]ResponseListener(nil), a.DeclaredListeners...), req.Listeners...)}

	cancelled := false
	var composition *tools.Composition

	finish := func(outcome Outcome, err error) {
		if err != nil {
			state.notifyAll("OnError", func(l ResponseListener) { l.OnError(err) })
		}
		state.notifyAll("OnFinished", func(l ResponseListener) { l.OnFinished(outcome) })
		if composition != nil && composition.Close != nil {
			composition.Close()
		}
	}
	if a.Model == nil {
		finish(OutcomeFailed, fmt.Errorf("golem: no chat model configured"))
		return
	}

	// onStart is where the registry fills; extra listeners join before any
	// other callback fires.
	state.notifyAll("OnStart", func(l ResponseListener) { l.OnStart(registry) })
	state.listeners = append(state.listeners, registry.extraListeners()...)

	if reason := registry.AbortReason(); reason != "" {
		finish(OutcomeFailed, fmt.Errorf("run aborted before it started: %s", reason))
		return
	}

	runCtx := req.context(ctx, registry.contextDecorators())

	// The ask machinery: one handler fanned out, or none at all.
	questionHandlers := registry.questionHandlersList()
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

	todoFan := tools.TodoFanOut(append(append([]tools.TodoEventHandler(nil), req.TodoHandlers...), registry.todoEventHandlers()...))

	if a.Provider == nil {
		finish(OutcomeFailed, fmt.Errorf("golem: no tools provider configured"))
		return
	}
	composition, err := a.Provider.Compose(runCtx, tools.ComposeRequest{
		ScenarioOffers:     req.Scenario.Offers,
		UserID:             req.UserID,
		ChatID:             req.ChatID,
		TodoHandler:        todoFan,
		Questions:          a.asking(runCtx, req, questionHandler),
		AnswersArriveLater: answersArriveLater,
		AskedMessage:       a.message(i18n.QuestionAsked),
		AskEnabled:         a.Config.AI.Tools.AskUserQuestion.Enabled,
	})
	if err != nil {
		finish(OutcomeFailed, fmt.Errorf("composing tools: %w", err))
		return
	}
	modelWithTools, err := a.Model.WithTools(composition.Info)
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
	content := &strings.Builder{}
	reportContent := func(soFar string) {
		state.notifyAll("OnContent", func(l ResponseListener) { l.OnContent(soFar) })
	}

	for iteration := 0; ; iteration++ {
		if ctx.Err() != nil || !state.shouldContinue() {
			cancelled = true
			break
		}
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
			if chunk.ResponseMeta != nil && chunk.ResponseMeta.Usage != nil && chunk.ResponseMeta.Usage.TotalTokens > 0 {
				usage := chunk.ResponseMeta.Usage
				name := modelName
				modelOnce.Do(func() {
					if name == "" {
						name = a.ModelName
					}
					modelName = name
					finalName := name
					state.notifyAll("OnModel", func(l ResponseListener) { l.OnModel(finalName) })
				})
				state.notifyAll("OnUsage", func(l ResponseListener) { l.OnUsage(modelName, usage) })
			}
		}
		reader.Close()
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
	}

	if a.Memory != nil && req.Scenario.ConversationMemory() && req.ConversationID != "" {
		if err := a.Memory.Append(runCtx, req.ConversationID, newMessages); err != nil {
			a.Log.Error("appending chat memory", "conversation", req.ConversationID, "err", err)
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
	system, err := prompt.Render(a.Config.AI.SystemPrompt, vars)
	if err != nil {
		return nil, fmt.Errorf("rendering system prompt: %w", err)
	}
	messages := []*schema.Message{{Role: schema.System, Content: system}}
	if a.Memory != nil && req.Scenario.ConversationMemory() && req.ConversationID != "" {
		history, err := a.Memory.Load(ctx, req.ConversationID, a.MemoryWindow)
		if err != nil {
			return nil, fmt.Errorf("loading chat memory: %w", err)
		}
		messages = append(messages, history...)
	}
	return messages, nil
}

// message resolves a localized runtime string.
func (a *Agent) message(key string) string {
	if a.Messages != nil {
		return a.Messages.Get(key)
	}
	return key
}

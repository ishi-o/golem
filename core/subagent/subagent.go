// Package subagent is the agent run as a tool of its own: work that would
// fill this run's context — reading a long file, sweeping a cluster, trying
// three things to see which holds — is handed to a run that has a context of
// its own, and comes back as one answer.
//
// Starting one does not wait for it, so several can be in the air at once and
// the model decides when it wants each answer. What it cannot do is walk
// away: the agent holds a run open until the subagents it started have
// finished, so an answer nobody collects is still paid for and still
// reported. The tool descriptions say so.
//
// The package lives outside core/agent for the same reason core/schedule
// does: the tools need the agent itself, and the agent depends on the tools.
package subagent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/i18n"
	"github.com/ishi-o/golem/core/prompt"
	"github.com/ishi-o/golem/core/tools"
)

// Tools are the subagent tools: start, wait, cancel. Register puts them on
// the provider and teaches the agent to forget a run's subagents when the
// run ends — also the last moment one of them can still be running, so
// nothing here outlives the turn that put it there, and a subagent id from
// an earlier turn is simply unknown.
type Tools struct {
	agent    *agent.Agent
	cfg      config.Config
	messages *i18n.Bundle
	log      *slog.Logger

	mu sync.Mutex
	// subagentsOf maps each run's request id to the subagents it started.
	// A run's entry is dropped when the run ends.
	subagentsOf map[string]map[string]*subRun
}

// subRun is one subagent, and what its listener collected: the answer so
// far, the failure if it failed, the outcome once it has one.
type subRun struct {
	id          string
	description string
	// startedAt is what the total wait is measured against — on the start,
	// not on a wait, because the model may wait, do something else and wait
	// again: what is bounded is how long the subagent may take, not how long
	// one call sat there.
	startedAt time.Time

	mu      sync.Mutex
	content string
	runErr  error
	outcome agent.Outcome
	done    chan struct{}
}

func (s *subRun) recordContent(soFar string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.content = soFar
}

func (s *subRun) recordError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runErr = err
}

func (s *subRun) recordFinished(outcome agent.Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outcome != "" {
		return
	}
	s.outcome = outcome
	close(s.done)
}

// snapshot reads the collected state under one lock.
func (s *subRun) snapshot() (content string, runErr error, outcome agent.Outcome) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.content, s.runErr, s.outcome
}

func (s *subRun) running() bool {
	_, _, outcome := s.snapshot()
	return outcome == ""
}

// Register constructs the subagent tools and registers them. Call it during
// application setup, before the first Fire; the SUBAGENT scenario excludes
// them by name, which is the depth cap — a subagent cannot start one of its
// own.
func Register(p *tools.Provider, a *agent.Agent, cfg config.Config, messages *i18n.Bundle, log *slog.Logger) *Tools {
	_ = cfg.Normalize()
	if messages == nil {
		messages = i18n.New(cfg.Locale, log)
	}
	if log == nil {
		log = slog.Default()
	}
	t := &Tools{
		agent:       a,
		cfg:         cfg,
		messages:    messages,
		log:         log,
		subagentsOf: map[string]map[string]*subRun{},
	}
	for _, tl := range t.List() {
		p.Register(tl, nil)
	}
	// How the registry learns a run has ended: a default listener that
	// attaches a forgetting listener to each run as it starts.
	a.AddDefaultListener(agent.ListenerFuncs{OnStartFunc: t.forgetWhenFinished})
	return t
}

// List lists the subagent tools.
func (t *Tools) List() []tool.InvokableTool {
	return []tool.InvokableTool{t.start(), t.wait(), t.cancel()}
}

func (t *Tools) forgetWhenFinished(run *agent.RunContext) {
	requestID := run.Request().RequestID
	if requestID == "" {
		return
	}
	run.AddListener(agent.ListenerFuncs{OnFinishedFunc: func(agent.Outcome) {
		t.mu.Lock()
		forgotten := len(t.subagentsOf[requestID])
		delete(t.subagentsOf, requestID)
		t.mu.Unlock()
		if forgotten > 0 {
			t.log.Info("run ended, forgetting its subagents", "run", requestID, "count", forgotten)
		}
	}})
}

func (t *Tools) start() tool.InvokableTool {
	type input struct {
		// Description is one line in active voice saying what the subagent
		// is for, shown to the user while it works.
		Description string `json:"description"`
		// Prompt is the subagent's whole brief: the task, every fact it
		// needs, and what to report back.
		Prompt string `json:"prompt"`
	}
	return tools.MustTool(utils.InferTool(tools.ToolNameStartSubagent,
		"Start a subagent: another run of yourself, with its own context window and the same tools, working on one task you describe. Use it for work whose middle you do not need to see — reading a long file or transcript to answer one question about it, sweeping several repositories or clusters, or trying an approach that may take many steps — so that only the answer lands in this conversation. Start several and they run at the same time.\n"+
			"The subagent sees nothing of this conversation: not the user message, not what you have found so far, not the files you have open. Everything it needs goes in the prompt, written as a self-contained brief, and it should say what to report back.\n"+
			"It cannot ask the user anything, so tell it what to assume. It cannot start subagents of its own or schedule tasks. It writes to the same workspace you do, so a file it leaves behind is a file you can read.\n"+
			"This returns at once with an id. Collect the answer with WaitForSubagent before you finish your turn — a subagent you never wait for still runs to the end and still costs its tokens, so if you no longer want it, call CancelSubagent.",
		func(ctx context.Context, in input) (string, error) {
			parentID, err := tools.RequestID.Require(ctx)
			if err != nil {
				return "", err
			}
			userID, err := tools.UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			chatID, _ := tools.ChatID.Get(ctx)
			chatType, _ := tools.ChatType.Get(ctx)

			if strings.TrimSpace(in.Prompt) == "" {
				return t.message("subagent-empty-prompt"), nil
			}
			if !t.agent.Accepting() {
				return t.message("subagent-shutting-down"), nil
			}

			max := t.cfg.AI.Tools.Subagent.MaxConcurrent
			t.mu.Lock()
			subagents := t.subagentsOf[parentID]
			if subagents == nil {
				subagents = map[string]*subRun{}
				t.subagentsOf[parentID] = subagents
			}
			running := 0
			for _, s := range subagents {
				if s.running() {
					running++
				}
			}
			if running >= max {
				t.mu.Unlock()
				return t.message("subagent-too-many", max), nil
			}
			sub := &subRun{
				id:          newSubagentID(),
				description: in.Description,
				startedAt:   time.Now(),
				done:        make(chan struct{}),
			}
			subagents[sub.id] = sub
			t.mu.Unlock()

			t.log.Info("starting subagent", "subagent", sub.id, "run", parentID, "description", in.Description)
			req := agent.NewRequest(agent.SubagentScenario, t.subagentPrompt(in.Prompt),
				// What ties the two runs together: cancelling this run
				// cancels the subagent, its tokens are counted on this turn,
				// and this run is held open until the subagent has finished.
				agent.WithRequestID(sub.id),
				agent.WithParent(parentID),
				agent.WithDescription(in.Description),
				agent.WithIdentity(userID, chatID, chatType),
				// A conversation of its own, not the parent's: the scenario
				// attaches no memory either way, and this keeps it out of
				// every store whatever the backend does.
				agent.WithConversation(sub.id, "", ""),
				// No card, no stop button, no output of its own interleaved
				// with this conversation, and no ask offered.
				agent.WithBackground(true),
				agent.WithListener(agent.ListenerFuncs{
					OnContentFunc: sub.recordContent,
					OnErrorFunc:   sub.recordError,
					OnFinishedFunc: func(o agent.Outcome) {
						sub.recordFinished(o)
					},
				}),
			)
			if err := t.agent.Fire(req); err != nil {
				// The entry above is what WaitForSubagent waits on, and only
				// a run that was actually started ever releases it — a fire
				// that failed would otherwise leave a subagent that is
				// forever running, holding a place under the limit and
				// hanging the first wait for it.
				t.mu.Lock()
				delete(subagents, sub.id)
				t.mu.Unlock()
				t.log.Error("could not start subagent", "subagent", sub.id, "run", parentID, "err", err)
				return t.message("subagent-could-not-start", err.Error()), nil
			}
			return t.message("subagent-started", sub.id, in.Description), nil
		}))
}

func (t *Tools) wait() tool.InvokableTool {
	type input struct {
		SubagentID string `json:"subagentId"`
	}
	return tools.MustTool(utils.InferTool(tools.ToolNameWaitSubagent,
		"Wait for a subagent to finish and read its answer. Wait for each subagent you started, one after another, before you finish your turn.\n"+
			"This blocks until the subagent finishes or until a while has passed, whichever comes first. If it is still working you are told so and told for how long, and you call this again to go on waiting — a subagent does not stop or lose anything because a wait for it came back. Keep waiting for as long as the work is worth; if it no longer is, call CancelSubagent instead.",
		func(ctx context.Context, in input) (string, error) {
			sub := t.subagentOf(ctx, in.SubagentID)
			if sub == nil {
				return t.message("subagent-unknown", in.SubagentID), nil
			}
			cfg := t.cfg.AI.Tools.Subagent
			select {
			case <-sub.done:
			case <-ctx.Done():
				// The run doing the waiting was cancelled; the subagent does
				// not stop or lose anything.
				return t.message("subagent-interrupted", in.SubagentID), nil
			case <-time.After(cfg.WaitPoll):
				waited := time.Since(sub.startedAt)
				if waited < cfg.WaitTimeout {
					t.log.Info("subagent still running, handing the turn back to the model",
						"subagent", in.SubagentID, "waited", waited)
					return t.message("subagent-still-running", in.SubagentID, sub.description, humanize(waited)), nil
				}
				// Past the ceiling this is a fault, not slow work: something
				// is holding the run open that nothing else is going to
				// release.
				t.log.Error("subagent did not finish within the ceiling; cancelling it",
					"subagent", in.SubagentID, "timeout", cfg.WaitTimeout)
				t.agent.Cancel(in.SubagentID)
				return t.message("subagent-wait-timed-out", in.SubagentID, humanize(cfg.WaitTimeout)), nil
			}
			content, runErr, outcome := sub.snapshot()
			switch outcome {
			case agent.OutcomeCompleted:
				if strings.TrimSpace(content) == "" {
					return t.message("subagent-no-answer", in.SubagentID), nil
				}
				return t.message("subagent-answer", in.SubagentID, sub.description) + "\n\n" + content, nil
			case agent.OutcomeCancelled:
				return t.message("subagent-cancelled", in.SubagentID), nil
			default: // FAILED
				reason := "unknown"
				if runErr != nil && runErr.Error() != "" {
					reason = runErr.Error()
				}
				return t.message("subagent-failed", in.SubagentID, reason), nil
			}
		}))
}

func (t *Tools) cancel() tool.InvokableTool {
	type input struct {
		SubagentID string `json:"subagentId"`
	}
	return tools.MustTool(utils.InferTool(tools.ToolNameCancelSubagent,
		"Stop a subagent you no longer need the answer from, so it does not keep working and spending tokens on it. What it had already done is lost; there is no answer to collect afterwards. A subagent that has already finished is left alone.",
		func(ctx context.Context, in input) (string, error) {
			sub := t.subagentOf(ctx, in.SubagentID)
			if sub == nil {
				return t.message("subagent-unknown", in.SubagentID), nil
			}
			if !sub.running() {
				_, _, outcome := sub.snapshot()
				return t.message("subagent-already-finished", in.SubagentID, string(outcome)), nil
			}
			// Cooperative, as every cancel here is: the run stops at its
			// next emission. Its own listener still reports it finished, so
			// a wait already outstanding on it comes back rather than
			// hanging.
			t.agent.Cancel(in.SubagentID)
			return t.message("subagent-cancel-requested", in.SubagentID), nil
		}))
}

// subagentOf is the subagent of the calling run under that id, or nil. Only
// its own: an id belongs to the run that started it, so one conversation
// cannot read or stop another conversation's work by naming it.
func (t *Tools) subagentOf(ctx context.Context, subagentID string) *subRun {
	parentID, err := tools.RequestID.Require(ctx)
	if err != nil || subagentID == "" {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.subagentsOf[parentID][subagentID]
}

// subagentPrompt renders the configured template over the one variable a
// subagent has, the brief it was given. A blown-up template does not stop
// the subagent: a task that cannot be introduced is still worth running, so
// its brief goes to the model unwrapped.
func (t *Tools) subagentPrompt(brief string) string {
	rendered, err := prompt.Render(t.cfg.AI.SubagentPrompt, map[string]string{"taskText": brief})
	if err != nil {
		t.log.Error("failed to render the subagent prompt, sending the brief as-is", "err", err)
		return brief
	}
	return rendered
}

func (t *Tools) message(key string, args ...any) string {
	return t.messages.Get(key, args...)
}

// humanize is a duration as the model should read it back to the user,
// rather than as "1m30.5s".
func humanize(d time.Duration) string {
	seconds := int(d.Seconds())
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	return fmt.Sprintf("%dm", int(d.Minutes()))
}

// newSubagentID mints an unguessable subagent id.
func newSubagentID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		panic("golem: cannot read random bytes for a subagent id: " + err.Error())
	}
	return "sub_" + hex.EncodeToString(b)
}

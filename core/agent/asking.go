package agent

import (
	"context"
	"strings"

	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/i18n"
	"github.com/ishi-o/golem/core/tools"
)

// asking wraps the run's question handlers with the outstanding-ask guard:
// one unanswered ask per conversation, whatever channel asked it. A second
// ask while one is pending fails with the model-facing already-asked
// instruction — two forms on two cards for the same question is the failure
// this prevents.
//
// handler may be nil (no surface registered one); the ask tool is then not
// offered at all, so asking is never reached without a handler.
func (a *Agent) asking(ctx context.Context, req Request, handler tools.QuestionHandler) tools.QuestionHandler {
	if handler == nil {
		return nil
	}
	return guardedHandler{a: a, req: req, delegate: handler}
}

type guardedHandler struct {
	a        *Agent
	req      Request
	delegate tools.QuestionHandler
}

func (g guardedHandler) Ask(ctx context.Context, questions []tools.Question) (map[string]string, error) {
	// The guard reads the pending-question store; a repo failure is logged
	// and the ask proceeds, because asking twice is a smaller failure than
	// not asking at all.
	if g.a.Repos != nil && g.req.ConversationID != "" {
		pending, err := g.a.Repos.PendingQuestions().FindByConversationIDAndStatus(ctx, g.req.ConversationID, dao.PendingQuestionStatusPending)
		if err != nil {
			g.a.Log.Warn("outstanding-ask guard could not read pending questions; allowing the ask", "err", err)
		} else if len(pending) > 0 {
			return nil, &tools.ErrNotAnswered{Message: g.a.message(i18n.QuestionAlreadyAsked)}
		}
	}
	return g.delegate.Ask(ctx, questions)
}

// fanOutQuestions merges handlers: every handler is asked, and the answers
// are merged first-answer-wins. A handler that cannot present returns
// ErrNotAnswered (counted as presented — the questions did reach a
// surface); any other handler error is logged and that handler skipped.
// Zero handlers presenting is the cannot-ask instruction.
func (a *Agent) fanOutQuestions(ctx context.Context, handlers []tools.QuestionHandler) tools.QuestionHandler {
	return questionFan{a: a, handlers: handlers}
}

type questionFan struct {
	a        *Agent
	handlers []tools.QuestionHandler
}

func (f questionFan) Ask(ctx context.Context, questions []tools.Question) (map[string]string, error) {
	answers := map[string]string{}
	presented := 0
	var settled *tools.ErrNotAnswered
	for _, h := range f.handlers {
		got, err := h.Ask(ctx, questions)
		switch {
		case tools.AsErrNotAnswered(err) != nil:
			presented++
			if settled == nil {
				settled = tools.AsErrNotAnswered(err)
			}
		case err != nil:
			// A handler erroring past ErrNotAnswered is a bug in that
			// handler; it costs its channel, not the ask.
			f.a.Log.Error("question handler failed", "err", err)
		default:
			presented++
			for k, v := range got {
				if _, exists := answers[k]; !exists {
					answers[k] = v
				}
			}
		}
	}
	if presented == 0 {
		return nil, &tools.ErrNotAnswered{Message: f.a.message(i18n.QuestionCannotAsk)}
	}
	if len(answers) > 0 {
		return answers, nil
	}
	if settled != nil {
		return nil, settled
	}
	// Every handler presented and returned nothing: the user-facing shape of
	// "questions are out, no answer in this run" — the joined headers, for
	// a surface that renders the error to the user.
	var headers []string
	for _, q := range questions {
		if q.Header != "" {
			headers = append(headers, q.Header)
		} else {
			headers = append(headers, q.Question)
		}
	}
	return nil, &tools.ErrNotAnswered{Message: strings.Join(headers, ", ")}
}

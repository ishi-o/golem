package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// Question is one question the model puts to the user.
type Question struct {
	// Question is the text as the model phrased it.
	Question string `json:"question"`
	// Header is the short label a surface shows next to the answer channel,
	// used when the questions are rendered where the model is not.
	Header string `json:"header,omitempty"`
	// Options are the answers to choose among, safest first — the default
	// prompt asks the model to order them that way.
	Options []string `json:"options"`
	// MultiSelect allows more than one choice.
	MultiSelect bool `json:"multiSelect,omitempty"`
	// Other, when non-empty, offers a free-text answer alongside the
	// options, labelled with this text.
	Other string `json:"other,omitempty"`
}

// Questions is a whole ask: one or more questions presented together.
type Questions struct {
	Questions []Question `json:"questions"`
}

// QuestionHandler presents questions to the user and, on the synchronous
// flavour, returns their answers within the call. A handler that cannot
// present them returns ErrNotAnswered — anything else is a bug in the
// handler, and the ask machinery needs to tell the two apart.
type QuestionHandler interface {
	Ask(ctx context.Context, questions []Question) (map[string]string, error)
}

// InlineAnswers is the capability a handler implements when it returns the
// answer inside the Ask call — the CLI's terminal prompt, say. Without it,
// the ask ends the turn: the answers can only arrive after the run is over
// (a form filled minutes later), so the model cannot be allowed to wait on
// them. This is the Go shape of spring-agent's SynchronousQuestionHandler
// marker interface, read before the ask runs.
type InlineAnswers interface {
	QuestionHandler

	// AnswersInline reports whether Ask blocks until the user replied.
	AnswersInline() bool
}

// ErrNotAnswered is the error the ask machinery and handlers use for "the
// questions were put to the user (or could not be), and no answer is coming
// inside this run". Its message is model-facing instructions — the tool
// result the model reads — so constructing it with a user-facing string is
// a behavior choice, not a formatting slip.
type ErrNotAnswered struct {
	// Message is what the model sees in place of a real result.
	Message string
}

func (e *ErrNotAnswered) Error() string { return e.Message }

// AsErrNotAnswered unwraps the marker error, nil for anything else.
func AsErrNotAnswered(err error) *ErrNotAnswered {
	var target *ErrNotAnswered
	if errors.As(err, &target) {
		return target
	}
	return nil
}

// AskOptions parametrizes the ask tool for one run.
type AskOptions struct {
	// AnswersArriveLater is true when no handler can answer inline. The ask
	// then ends the turn: the result is recorded for the next run (which is
	// why AskedMessage matters) and the model is not asked to continue from
	// an answer it does not have.
	AnswersArriveLater bool

	// AskedMessage is the model-facing instruction recorded as the tool
	// result when the questions were presented and no answer came inside
	// the run — the i18n bundle's question-asked text. The next run reads
	// it in the history and knows an ask is outstanding.
	AskedMessage string
}

// AskUserQuestion is the ask tool: the model's one way to put questions to
// the person behind the run.
func AskUserQuestion(handler QuestionHandler, opts AskOptions) tool.InvokableTool {
	type input = Questions
	return mustTool(utils.InferTool(ToolNameAskUserQuestion,
		"Ask the user questions and get their answers. Use this before any action you cannot undo: deleting or overwriting something, reaching someone outside the conversation, or changing a live production system. Put the safest option first and say plainly what would be lost.",
		func(ctx context.Context, in input) (string, error) {
			if len(in.Questions) == 0 {
				return "", fmt.Errorf("no questions to ask")
			}
			answers, err := handler.Ask(ctx, in.Questions)
			if err != nil {
				return "", err
			}
			if len(answers) == 0 {
				// Presented, but the answers can only arrive in a future
				// run. The result doubles as the next run's history entry,
				// so it is written as instructions to that run.
				message := opts.AskedMessage
				if message == "" {
					message = "The questions are now with the user. This turn ends here; you will be started again with the answers."
				}
				if opts.AnswersArriveLater {
					return endTurnPrefix + message, nil
				}
				return message, nil
			}
			var b strings.Builder
			b.WriteString("The user answered:\n")
			for _, q := range in.Questions {
				if a, ok := answers[q.Question]; ok {
					fmt.Fprintf(&b, "- %s: %s\n", q.Question, a)
				}
			}
			if opts.AnswersArriveLater {
				return endTurnPrefix + b.String(), nil
			}
			return b.String(), nil
		}))
}

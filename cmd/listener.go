package cmd

import (
	"fmt"
	"io"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
)

type terminalListener struct {
	output io.Writer
}

func (l terminalListener) OnStart(*agent.RunRegistry)         {}
func (l terminalListener) OnSubscribe()                       {}
func (l terminalListener) OnModel(string)                     {}
func (l terminalListener) OnUsage(string, *schema.TokenUsage) {}
func (l terminalListener) OnError(err error)                  { _, _ = fmt.Fprintf(l.output, "error: %v\n", err) }
func (l terminalListener) OnFinished(agent.Outcome)           { _, _ = fmt.Fprintln(l.output) }
func (l terminalListener) ShouldContinue() bool               { return true }
func (l terminalListener) OnContent(content string) {
	_, _ = fmt.Fprintf(l.output, "\r%s", content)
}

type listenerWithQuestions struct {
	input  io.Reader
	output io.Writer
}

func (l listenerWithQuestions) OnStart(registry *agent.RunRegistry) {
	registry.AddQuestionHandler(terminalQuestions{input: l.input, output: l.output})
}
func (listenerWithQuestions) OnSubscribe()                       {}
func (listenerWithQuestions) OnModel(string)                     {}
func (listenerWithQuestions) OnContent(string)                   {}
func (listenerWithQuestions) OnUsage(string, *schema.TokenUsage) {}
func (listenerWithQuestions) OnError(error)                      {}
func (listenerWithQuestions) OnFinished(agent.Outcome)           {}
func (listenerWithQuestions) ShouldContinue() bool               { return true }

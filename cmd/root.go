// Package cmd builds the golem command line with Cobra. The command package
// owns terminal interaction only; callers inject the core runner so the CLI
// does not choose a model provider or persistence backend on their behalf.
package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/tools"
	"github.com/spf13/cobra"
)

// Runner is the small portion of the core runtime the CLI needs.
type Runner interface {
	Fire(agent.Request) error
	Cancel(requestID string) bool
}

// Config configures the command tree.
type Config struct {
	Runner  Runner
	UserID  string
	Session string
	Input   io.Reader
	Output  io.Writer
	Logger  *slog.Logger
}

// NewRoot creates the root command and its subcommands.
func NewRoot(config Config) *cobra.Command {
	if config.Input == nil {
		config.Input = strings.NewReader("")
	}
	if config.Output == nil {
		config.Output = io.Discard
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	if config.UserID == "" {
		config.UserID = "local"
	}
	if config.Session == "" {
		config.Session = "local"
	}

	root := &cobra.Command{Use: "golem", Short: "Run a golem agent", SilenceUsage: true}
	root.AddCommand(newChatCommand(config), newCancelCommand(config), newVersionCommand(config.Output))
	return root
}

func newChatCommand(config Config) *cobra.Command {
	var requestID string
	command := &cobra.Command{
		Use:   "chat [message]",
		Short: "Send a message to the agent",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(command *cobra.Command, args []string) error {
			if config.Runner == nil {
				return errors.New("golem chat: no agent runner configured")
			}
			message := ""
			if len(args) == 1 {
				message = args[0]
			} else {
				var err error
				message, err = readLine(config.Input)
				if err != nil {
					return err
				}
			}
			if strings.TrimSpace(message) == "" {
				return errors.New("golem chat: message is empty")
			}
			if requestID == "" {
				requestID = config.Session
			}
			listener := terminalListener{output: config.Output}
			request := agent.NewRequest(agent.ChatScenario, message,
				agent.WithRequestID(requestID),
				agent.WithIdentity(config.UserID, config.Session, "cli"),
				agent.WithConversation(config.Session, config.Session, requestID),
				agent.WithListener(listener),
			)
			request.Listeners = append(request.Listeners, listenerWithQuestions{input: config.Input, output: config.Output})
			return config.Runner.Fire(request)
		},
	}
	command.Flags().StringVar(&requestID, "request-id", "", "identifier used by cancel")
	return command
}

func newCancelCommand(config Config) *cobra.Command {
	return &cobra.Command{
		Use:   "cancel REQUEST_ID",
		Short: "Cancel a running request",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if config.Runner == nil {
				return errors.New("golem cancel: no agent runner configured")
			}
			if !config.Runner.Cancel(args[0]) {
				return fmt.Errorf("request %q is not running", args[0])
			}
			_, _ = fmt.Fprintf(config.Output, "cancelled %s\n", args[0])
			return nil
		},
	}
}

func newVersionCommand(output io.Writer) *cobra.Command {
	return &cobra.Command{Use: "version", Short: "Print the CLI version", Run: func(*cobra.Command, []string) { _, _ = fmt.Fprintln(output, "golem dev") }}
}

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

type terminalQuestions struct {
	input  io.Reader
	output io.Writer
}

func (q terminalQuestions) Ask(_ context.Context, questions []tools.Question) (map[string]string, error) {
	answers := make(map[string]string, len(questions))
	reader := bufio.NewReader(q.input)
	for _, question := range questions {
		_, _ = fmt.Fprintf(q.output, "\n%s\n", question.Question)
		for i, option := range question.Options {
			_, _ = fmt.Fprintf(q.output, "  %d) %s\n", i+1, option)
		}
		_, _ = fmt.Fprint(q.output, "> ")
		line, err := reader.ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			return nil, errors.New("no answer was entered")
		}
		answers[question.Question] = line
	}
	return answers, nil
}

func (terminalQuestions) AnswersInline() bool { return true }

func readLine(input io.Reader) (string, error) {
	line, err := bufio.NewReader(input).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

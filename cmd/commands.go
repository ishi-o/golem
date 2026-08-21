package cmd

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ishi-o/golem/core/agent"
	"github.com/spf13/cobra"
)

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
			done := make(chan struct{})
			listener := terminalListener{output: config.Output, done: done}
			request := agent.NewRequest(agent.ChatScenario, message,
				agent.WithRequestID(requestID),
				agent.WithIdentity(config.UserID, config.Session, "cli"),
				agent.WithConversation(config.Session, config.Session, requestID),
				agent.WithListener(listener),
			)
			request.Listeners = append(request.Listeners, listenerWithQuestions{input: config.Input, output: config.Output})
			if err := config.Runner.Fire(request); err != nil {
				return err
			}
			<-done
			return nil
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

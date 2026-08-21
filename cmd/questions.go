package cmd

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ishi-o/golem/core/tools"
)

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

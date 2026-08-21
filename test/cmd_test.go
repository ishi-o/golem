package agent_test

import (
	"strings"
	"testing"
	"time"

	"github.com/ishi-o/golem/cmd"
	"github.com/ishi-o/golem/core/agent"
	"github.com/stretchr/testify/require"
)

type blockingRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r *blockingRunner) Fire(request agent.Request) error {
	close(r.started)
	go func() {
		<-r.release
		for _, listener := range request.Listeners {
			listener.OnFinished(agent.OutcomeCompleted)
		}
	}()
	return nil
}

func (*blockingRunner) Cancel(string) bool { return false }

func TestChatWaitsForRunCompletion(t *testing.T) {
	runner := &blockingRunner{started: make(chan struct{}), release: make(chan struct{})}
	var output strings.Builder
	root := cmd.NewRoot(cmd.Config{
		Runner:  runner,
		Output:  &output,
		UserID:  "test-user",
		Session: "test-session",
	})
	root.SetArgs([]string{"chat", "hello"})

	done := make(chan error, 1)
	go func() { done <- root.Execute() }()

	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("chat did not start")
	}
	select {
	case err := <-done:
		t.Fatalf("chat returned before the listener finished: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(runner.release)
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("chat did not return after the listener finished")
	}
	require.Contains(t, output.String(), "\n")
}

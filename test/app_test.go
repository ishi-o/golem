package agent_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ishi-o/golem/app"
	"github.com/ishi-o/golem/core/agent"
	"github.com/stretchr/testify/require"
)

type httpAgentRunner struct {
	requests     chan agent.Request
	cancelled    chan string
	cancelResult bool
}

func (r *httpAgentRunner) Fire(request agent.Request) error {
	r.requests <- request
	for _, listener := range request.Listeners {
		listener.OnModel("test-model")
		listener.OnContent("hello from the test")
		listener.OnFinished(agent.OutcomeCompleted)
	}
	return nil
}

func (r *httpAgentRunner) Cancel(requestID string) bool {
	r.cancelled <- requestID
	return r.cancelResult
}

func TestHTTPChatStreamsRunEvents(t *testing.T) {
	runner := &httpAgentRunner{
		requests:  make(chan agent.Request, 1),
		cancelled: make(chan string, 1),
	}
	router := app.NewRouter(app.RouterConfig{Agent: runner})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/agent/chat", strings.NewReader(`{
		"message":"hello",
		"request_id":"run-1",
		"user_id":"user-1",
		"chat_id":"chat-1"
	}`))

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusOK, recorder.Code)
	require.Equal(t, "text/event-stream; charset=utf-8", recorder.Header().Get("Content-Type"))
	require.Equal(t, "run-1", recorder.Header().Get("X-Golem-Request-ID"))
	require.Contains(t, recorder.Body.String(), `event: started`)
	require.Contains(t, recorder.Body.String(), `event: content`)
	require.Contains(t, recorder.Body.String(), `"content":"hello from the test"`)
	require.Contains(t, recorder.Body.String(), `event: finished`)

	started := <-runner.requests
	require.Equal(t, "hello", started.Text)
	require.Equal(t, "user-1", started.UserID)
	require.Equal(t, "chat-1", started.ChatID)
	require.Equal(t, "chat-1", started.ConversationID)
}

func TestHTTPCancelRequestsRunCancellation(t *testing.T) {
	runner := &httpAgentRunner{
		requests:     make(chan agent.Request, 1),
		cancelled:    make(chan string, 1),
		cancelResult: true,
	}
	router := app.NewRouter(app.RouterConfig{Agent: runner})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/agent/cancel", strings.NewReader(`{"request_id":"run-1"}`))

	router.ServeHTTP(recorder, request)

	require.Equal(t, http.StatusAccepted, recorder.Code)
	require.JSONEq(t, `{"request_id":"run-1","status":"cancellation_requested"}`, recorder.Body.String())
	require.Equal(t, "run-1", <-runner.cancelled)
}

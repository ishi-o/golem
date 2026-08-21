package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/schema"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ishi-o/golem/core/agent"
)

const maxAgentRequestBytes = 1 << 20

// AgentRunner is the part of the agent runtime needed by the HTTP surface.
// *agent.Agent satisfies it, while the interface also keeps the router easy
// to exercise with a downstream adapter or a test double.
type AgentRunner interface {
	Fire(agent.Request) error
	Cancel(requestID string) bool
}

type agentHandler struct {
	runner AgentRunner
	log    *slog.Logger
}

type chatRequest struct {
	Message        string `json:"message"`
	RequestID      string `json:"request_id"`
	UserID         string `json:"user_id"`
	ChatID         string `json:"chat_id"`
	ConversationID string `json:"conversation_id"`
	RootMessageID  string `json:"root_message_id"`
	ReplyMessageID string `json:"reply_message_id"`
}

type cancelRequest struct {
	RequestID string `json:"request_id"`
}

type agentEvent struct {
	name string
	data any
}

// registerAgentRoutes adds the standard streaming chat and cancellation
// endpoints below the supplied router.
func registerAgentRoutes(router chi.Router, runner AgentRunner, logger *slog.Logger) {
	handler := &agentHandler{runner: runner, log: logger}
	router.Post("/chat", handler.chat)
	router.Post("/cancel", handler.cancel)
}

func (h *agentHandler) chat(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "streaming is not supported"})
		return
	}

	var input chatRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	if strings.TrimSpace(input.Message) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "message is required"})
		return
	}

	requestID := strings.TrimSpace(input.RequestID)
	if requestID == "" {
		requestID = middleware.GetReqID(r.Context())
	}
	if requestID == "" {
		var err error
		requestID, err = newRequestID()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
	}
	userID := strings.TrimSpace(input.UserID)
	if userID == "" {
		userID = "local"
	}
	chatID := strings.TrimSpace(input.ChatID)
	if chatID == "" {
		chatID = userID
	}
	conversationID := strings.TrimSpace(input.ConversationID)
	if conversationID == "" {
		conversationID = chatID
	}

	events := make(chan agentEvent, 32)
	listener := &streamListener{ctx: r.Context(), events: events}
	request := agent.NewRequest(
		agent.ChatScenario,
		input.Message,
		agent.WithRequestID(requestID),
		agent.WithIdentity(userID, chatID, "http"),
		agent.WithConversation(conversationID, input.RootMessageID, input.ReplyMessageID),
		agent.WithListener(listener),
	)
	if err := h.runner.Fire(request); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}

	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	w.Header().Set("X-Golem-Request-ID", requestID)
	w.WriteHeader(http.StatusOK)
	if err := writeSSE(w, agentEvent{name: "started", data: map[string]string{"request_id": requestID}}); err != nil {
		h.cancelAfterDisconnect(requestID)
		return
	}
	flusher.Flush()

	for {
		select {
		case event := <-events:
			if err := writeSSE(w, event); err != nil {
				h.cancelAfterDisconnect(requestID)
				return
			}
			flusher.Flush()
			if event.name == "finished" {
				return
			}
		case <-r.Context().Done():
			h.cancelAfterDisconnect(requestID)
			return
		}
	}
}

func (h *agentHandler) cancel(w http.ResponseWriter, r *http.Request) {
	var input cancelRequest
	if err := decodeJSON(w, r, &input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.RequestID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "request_id is required"})
		return
	}
	if !h.runner.Cancel(input.RequestID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "request not found"})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{
		"request_id": input.RequestID,
		"status":     "cancellation_requested",
	})
}

func (h *agentHandler) cancelAfterDisconnect(requestID string) {
	if h.runner.Cancel(requestID) && h.log != nil {
		h.log.Debug("cancelled disconnected agent request", "request_id", requestID)
	}
}

type streamListener struct {
	ctx    context.Context
	events chan<- agentEvent
}

func (l *streamListener) OnStart(*agent.RunContext) {}
func (l *streamListener) OnSubscribe()              {}

func (l *streamListener) OnModel(model string) {
	l.publish("model", map[string]string{"model": model})
}

func (l *streamListener) OnContent(content string) {
	l.publish("content", map[string]string{"content": content})
}

func (l *streamListener) OnUsage(model string, usage *schema.TokenUsage) {
	l.publish("usage", map[string]any{"model": model, "usage": usage})
}

func (l *streamListener) OnError(err error) {
	if err != nil {
		l.publish("error", map[string]string{"error": err.Error()})
	}
}

func (l *streamListener) OnFinished(outcome agent.Outcome) {
	l.publish("finished", map[string]string{"outcome": string(outcome)})
}

func (l *streamListener) ShouldContinue() bool {
	return l.ctx.Err() == nil
}

func (l *streamListener) publish(name string, data any) {
	select {
	case l.events <- agentEvent{name: name, data: data}:
	case <-l.ctx.Done():
	}
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAgentRequestBytes))
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("decode request: more than one JSON value")
		}
		return fmt.Errorf("decode request: %w", err)
	}
	return nil
}

func writeSSE(w io.Writer, event agentEvent) error {
	data, err := json.Marshal(event.data)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, data)
	return err
}

func newRequestID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate request id: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}

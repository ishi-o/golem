package feishu

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/tools"
	larkevent "github.com/larksuite/oapi-sdk-go/v3/event"
	larkcallback "github.com/larksuite/oapi-sdk-go/v3/event/dispatcher/callback"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// Handler receives Feishu event callbacks and turns message events into core
// agent requests. It acknowledges the webhook immediately; model work and
// replies continue asynchronously, which prevents Feishu's delivery retry
// from starting duplicate runs.
type Handler struct {
	agent             *agent.Agent
	backend           store.Backend
	client            *Client
	verificationToken string
	questionTTL       time.Duration
	// tenantID scopes every run this connector fires: a group's shared
	// files and skills are read through on top of each user's own. Empty
	// means no tenant scope.
	tenantID string
	logger   *slog.Logger
	clock    func() time.Time
	actionMu sync.Mutex
}

// HandlerOption configures a webhook handler during construction.
type HandlerOption func(*Handler)

// WithVerificationToken enables Feishu URL-verification and event-token
// checking.
func WithVerificationToken(token string) HandlerOption {
	return func(h *Handler) { h.verificationToken = token }
}

// WithQuestionTTL sets how long an interactive question remains answerable.
func WithQuestionTTL(ttl time.Duration) HandlerOption {
	return func(h *Handler) {
		if ttl > 0 {
			h.questionTTL = ttl
		}
	}
}

// WithClock supplies the clock used for question expiry.
func WithClock(now func() time.Time) HandlerOption {
	return func(h *Handler) {
		if now != nil {
			h.clock = now
		}
	}
}

// WithTenantID gives every run the connector fires a tenant scope: the
// tenant's shared files and skills are read through each user's own.
func WithTenantID(tenantID string) HandlerOption {
	return func(h *Handler) { h.tenantID = tenantID }
}

// NewHandler constructs a Feishu webhook handler.
func NewHandler(runtime *agent.Agent, backend store.Backend, client *Client, logger *slog.Logger, options ...HandlerOption) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	h := &Handler{agent: runtime, backend: backend, client: client, questionTTL: 24 * time.Hour, logger: logger, clock: time.Now}
	for _, option := range options {
		if option != nil {
			option(h)
		}
	}
	return h
}

// MessageEvent is the normalized subset of a Feishu message callback used by
// the runtime. Keeping it exported makes adapters and tests able to construct
// an event without reproducing Feishu's nested wire envelope.
type MessageEvent struct {
	EventID         string
	MessageID       string
	UserID          string
	ChatID          string
	ChatType        string
	RootMessageID   string
	ParentMessageID string
	Text            string
}

// DecodeMessageEvent decodes a Feishu v2 message callback.
func DecodeMessageEvent(data []byte) (MessageEvent, error) {
	var envelope larkim.P2MessageReceiveV1
	if err := json.Unmarshal(data, &envelope); err != nil {
		return MessageEvent{}, fmt.Errorf("feishu: decode event: %w", err)
	}
	if envelope.Event == nil || envelope.Event.Message == nil {
		return MessageEvent{}, errors.New("feishu: message event has no message")
	}
	message := envelope.Event.Message
	eventID := ""
	if envelope.EventV2Base != nil && envelope.EventV2Base.Header != nil {
		eventID = envelope.EventV2Base.Header.EventID
	}
	userID := ""
	if envelope.Event.Sender != nil && envelope.Event.Sender.SenderId != nil {
		userID = firstNonEmpty(
			valueString(envelope.Event.Sender.SenderId.OpenId),
			valueString(envelope.Event.Sender.SenderId.UserId),
		)
	}
	return MessageEvent{
		EventID:         firstNonEmpty(eventID, valueString(message.MessageId)),
		MessageID:       valueString(message.MessageId),
		UserID:          userID,
		ChatID:          valueString(message.ChatId),
		ChatType:        firstNonEmpty(valueString(message.ChatType), "p2p"),
		RootMessageID:   valueString(message.RootId),
		ParentMessageID: valueString(message.ParentId),
		Text:            decodeMessageText(valueString(message.Content)),
	}, nil
}

// ServeHTTP handles URL verification, message receive events, and card action
// callbacks. Unknown event types are acknowledged so Feishu does not retry a
// callback this connector intentionally does not consume.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	data, err := io.ReadAll(io.LimitReader(r.Body, 2<<20))
	if err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return
	}
	var envelope larkevent.EventFuzzy
	if err := json.Unmarshal(data, &envelope); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	eventType := ""
	token := envelope.Token
	if envelope.Header != nil {
		eventType = envelope.Header.EventType
		token = firstNonEmpty(token, envelope.Header.Token)
	}
	if h.verificationToken != "" && token != h.verificationToken {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if envelope.Type == "url_verification" {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": envelope.Challenge})
		return
	}
	if strings.Contains(eventType, "card.action") {
		var actionEvent larkcallback.CardActionTriggerEvent
		if err := json.Unmarshal(data, &actionEvent); err != nil {
			http.Error(w, "invalid action", http.StatusBadRequest)
			return
		}
		var actionValue map[string]any
		var actorID string
		if actionEvent.Event != nil {
			if actionEvent.Event.Action != nil {
				actionValue = actionEvent.Event.Action.Value
			}
			if actionEvent.Event.Operator != nil {
				actorID = firstNonEmpty(actionEvent.Event.Operator.OpenID, valueString(actionEvent.Event.Operator.UserID))
			}
		}
		if err := h.handleAction(r.Context(), actionValue, actorID); err != nil {
			h.logger.Error("Feishu card action failed", "err", err)
			http.Error(w, "action failed", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if eventType != "" && eventType != "im.message.receive_v1" {
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}
	event, err := DecodeMessageEvent(data)
	if err != nil || event.MessageID == "" || event.Text == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}
	if h.backend != nil {
		claimed, claimErr := h.backend.ProcessedMessages().Claim(r.Context(), event.MessageID)
		if claimErr != nil {
			h.logger.Error("Feishu duplicate guard failed", "messageId", event.MessageID, "err", claimErr)
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		if !claimed {
			writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
			return
		}
	}
	if h.agent == nil {
		if h.backend != nil {
			_ = h.backend.ProcessedMessages().Release(r.Context(), event.MessageID)
		}
		http.Error(w, "agent is not configured", http.StatusServiceUnavailable)
		return
	}
	rootID := event.RootMessageID
	if rootID == "" {
		rootID = event.MessageID
	}
	conversationID := firstNonEmpty(event.RootMessageID, event.ChatID, event.MessageID)
	listener := h.responseListener(event, true)
	// A group chat is the group: its id scopes the run, so the group's
	// shared files and skills are read through each member's own.
	groupID := ""
	if event.ChatType == "group" {
		groupID = event.ChatID
	}
	request := agent.NewRequest(agent.ChatScenario, event.Text,
		agent.WithRequestID(event.MessageID),
		agent.WithIdentity(event.UserID, event.ChatID, event.ChatType),
		agent.WithScope(groupID, h.tenantID),
		agent.WithConversation(conversationID, rootID, event.MessageID),
		agent.WithListener(listener),
	)
	// A message arriving while this conversation's run is still working
	// joins that run at its next tool boundary — a correction reaches the
	// turn it corrects. The duplicate claim above stands either way: the
	// message is answered once, by the run that read it or by its own.
	text := event.Text
	if h.agent.FireOrQueue(request, func() string { return text }, event.Text) {
		writeJSON(w, http.StatusOK, map[string]bool{"queued": true})
		return
	}
	if err := h.agent.Fire(request); err != nil {
		if h.backend != nil {
			_ = h.backend.ProcessedMessages().Release(r.Context(), event.MessageID)
		}
		h.logger.Error("Feishu agent request rejected", "messageId", event.MessageID, "err", err)
		http.Error(w, "agent unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

func (h *Handler) responseListener(event MessageEvent, releaseClaim bool) agent.ResponseListener {
	var content string
	var runErr error
	return agent.ListenerFuncs{
		OnStartFunc: func(run *agent.RunContext) {
			if h.client != nil && h.backend != nil {
				run.AddQuestionHandler(&questionHandler{handler: h, event: event})
			}
		},
		OnContentFunc: func(value string) { content = value },
		OnErrorFunc:   func(err error) { runErr = err },
		OnFinishedFunc: func(outcome agent.Outcome) {
			if releaseClaim && outcome != agent.OutcomeCompleted && h.backend != nil && event.MessageID != "" {
				if err := h.backend.ProcessedMessages().Release(context.Background(), event.MessageID); err != nil {
					h.logger.Error("Feishu duplicate claim release failed", "messageId", event.MessageID, "err", err)
				}
			}
			if h.client == nil || event.ChatID == "" {
				return
			}
			if outcome == agent.OutcomeCancelled {
				return
			}
			if runErr != nil {
				content = "The agent could not finish this request: " + runErr.Error()
			}
			if strings.TrimSpace(content) == "" {
				return
			}
			if _, err := h.client.ReplyText(context.Background(), event.MessageID, content); err != nil {
				h.logger.Error("Feishu reply failed", "messageId", event.MessageID, "err", err)
			}
		},
	}
}

type questionHandler struct {
	handler *Handler
	event   MessageEvent
}

func (q *questionHandler) Ask(ctx context.Context, questions []tools.Question) (map[string]string, error) {
	if q.handler.client == nil || q.handler.backend == nil {
		return nil, &tools.ErrNotAnswered{Message: "Questions could not be shown in Feishu."}
	}
	pendingID := randomID()
	card := questionCard(pendingID, questions)
	cardMessageID, err := q.handler.client.SendCard(ctx, ReceiveIDChatID, q.event.ChatID, card)
	if err != nil {
		return nil, err
	}
	questionsJSON, err := json.Marshal(questions)
	if err != nil {
		return nil, err
	}
	ttl := q.handler.questionTTL
	if ttl <= 0 || ttl > 14*24*time.Hour {
		ttl = 14 * 24 * time.Hour
	}
	now := q.handler.now()
	question := store.PendingQuestion{ID: pendingID, UserID: q.event.UserID, ChatID: q.event.ChatID, ChatType: q.event.ChatType, ConversationID: firstNonEmpty(q.event.RootMessageID, q.event.ChatID, q.event.MessageID), RootMessageID: firstNonEmpty(q.event.RootMessageID, q.event.MessageID), CardID: cardMessageID, QuestionsJSON: string(questionsJSON), Status: store.PendingQuestionStatusPending, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := q.handler.backend.PendingQuestions().Save(ctx, question); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

func (h *Handler) handleAction(ctx context.Context, value map[string]any, actorID string) error {
	if h.backend == nil {
		return errors.New("feishu: persistence is not configured")
	}
	// Feishu can retry a card action while the first callback is still
	// entering the agent. Serializing the local read-and-claim closes that
	// window; the persisted status then keeps later callbacks out as well.
	h.actionMu.Lock()
	defer h.actionMu.Unlock()
	pendingID, _ := value["pending_id"].(string)
	question, _ := value["question"].(string)
	answer, _ := value["answer"].(string)
	if pendingID == "" || answer == "" {
		return errors.New("feishu: card action has no pending_id or answer")
	}
	pending, err := h.backend.PendingQuestions().Get(ctx, pendingID)
	if err != nil {
		return err
	}
	if pending == nil || pending.Status != store.PendingQuestionStatusPending {
		return errors.New("feishu: pending question is no longer answerable")
	}
	if actorID != "" && pending.UserID != "" && actorID != pending.UserID {
		return errors.New("feishu: card action is from a different user")
	}
	if !pending.ExpiresAt.IsZero() && !pending.ExpiresAt.After(h.now()) {
		return h.backend.PendingQuestions().SetStatus(ctx, pending.ID, store.PendingQuestionStatusExpired)
	}
	if err := h.backend.PendingQuestions().SetStatus(ctx, pending.ID, store.PendingQuestionStatusAnswered); err != nil {
		return err
	}
	if h.agent == nil {
		_ = h.backend.PendingQuestions().SetStatus(ctx, pending.ID, store.PendingQuestionStatusPending)
		return errors.New("feishu: agent is not configured")
	}
	text := fmt.Sprintf("The user answered the question %q with: %s", question, answer)
	// The same scope the asking run carried: a resumed answer in a group
	// chat reads through the group's home too.
	resumeGroupID := ""
	if pending.ChatType == "group" {
		resumeGroupID = pending.ChatID
	}
	err = h.agent.Fire(agent.NewRequest(agent.ChatScenario, text,
		agent.WithRequestID(pending.ID+":answer"),
		agent.WithIdentity(pending.UserID, pending.ChatID, pending.ChatType),
		agent.WithScope(resumeGroupID, h.tenantID),
		agent.WithConversation(pending.ConversationID, pending.RootMessageID, pending.CardID),
		agent.WithListener(h.responseListener(MessageEvent{MessageID: pending.CardID, UserID: pending.UserID, ChatID: pending.ChatID, ChatType: pending.ChatType, RootMessageID: pending.RootMessageID}, false)),
	))
	if err != nil {
		_ = h.backend.PendingQuestions().SetStatus(ctx, pending.ID, store.PendingQuestionStatusPending)
	}
	return err
}

func questionCard(pendingID string, questions []tools.Question) map[string]any {
	elements := make([]map[string]any, 0, len(questions)*2)
	for _, question := range questions {
		elements = append(elements, map[string]any{"tag": "markdown", "content": question.Question})
		actions := make([]map[string]any, 0, len(question.Options))
		for _, option := range question.Options {
			actions = append(actions, map[string]any{"tag": "button", "text": map[string]any{"tag": "plain_text", "content": option}, "type": "default", "value": map[string]any{"pending_id": pendingID, "question": question.Question, "answer": option}})
		}
		if len(actions) > 0 {
			elements = append(elements, map[string]any{"tag": "action", "actions": actions})
		}
	}
	return map[string]any{"config": map[string]any{"wide_screen_mode": true}, "elements": elements}
}

func (h *Handler) now() time.Time {
	if h.clock != nil {
		return h.clock()
	}
	return time.Now()
}

func randomID() string {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return fmt.Sprintf("pending-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(data)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func valueString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func decodeMessageText(content string) string {
	if content == "" {
		return ""
	}
	var message struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &message); err != nil {
		return strings.TrimSpace(content)
	}
	return strings.TrimSpace(message.Text)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

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
	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/tools"
)

// Handler receives Feishu event callbacks and turns message events into core
// agent requests. It acknowledges the webhook immediately; model work and
// replies continue asynchronously, which prevents Feishu's delivery retry
// from starting duplicate runs.
type Handler struct {
	Agent             *agent.Agent
	Backend           dao.Backend
	Client            *Client
	VerificationToken string
	QuestionTTL       time.Duration
	Logger            *slog.Logger
	Now               func() time.Time
	actionMu          sync.Mutex
}

// NewHandler wires a Feishu webhook handler.
func NewHandler(runtime *agent.Agent, backend dao.Backend, client *Client, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{Agent: runtime, Backend: backend, Client: client, QuestionTTL: 24 * time.Hour, Logger: logger, Now: time.Now}
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

type callbackEnvelope struct {
	Type      string `json:"type"`
	Token     string `json:"token"`
	Challenge string `json:"challenge"`
	Header    struct {
		EventID   string `json:"event_id"`
		EventType string `json:"event_type"`
		Token     string `json:"token"`
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
				UserID string `json:"user_id"`
			} `json:"sender_id"`
		} `json:"sender"`
		Message struct {
			MessageID   string `json:"message_id"`
			RootID      string `json:"root_id"`
			ParentID    string `json:"parent_id"`
			ChatID      string `json:"chat_id"`
			ChatType    string `json:"chat_type"`
			MessageType string `json:"message_type"`
			Content     string `json:"content"`
		} `json:"message"`
		Action struct {
			Value map[string]any `json:"value"`
		} `json:"action"`
		Operator struct {
			OpenID string `json:"open_id"`
			UserID string `json:"user_id"`
		} `json:"operator"`
	} `json:"event"`
}

// DecodeMessageEvent decodes a Feishu v2 message callback.
func DecodeMessageEvent(data []byte) (MessageEvent, error) {
	var envelope callbackEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return MessageEvent{}, fmt.Errorf("feishu: decode event: %w", err)
	}
	content := struct {
		Text string `json:"text"`
	}{}
	if envelope.Event.Message.Content != "" {
		if err := json.Unmarshal([]byte(envelope.Event.Message.Content), &content); err != nil {
			content.Text = envelope.Event.Message.Content
		}
	}
	userID := envelope.Event.Sender.SenderID.OpenID
	if userID == "" {
		userID = envelope.Event.Sender.SenderID.UserID
	}
	return MessageEvent{EventID: firstNonEmpty(envelope.Header.EventID, envelope.Event.Message.MessageID), MessageID: envelope.Event.Message.MessageID, UserID: userID, ChatID: envelope.Event.Message.ChatID, ChatType: firstNonEmpty(envelope.Event.Message.ChatType, "p2p"), RootMessageID: envelope.Event.Message.RootID, ParentMessageID: envelope.Event.Message.ParentID, Text: strings.TrimSpace(content.Text)}, nil
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
	var envelope callbackEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		http.Error(w, "invalid event", http.StatusBadRequest)
		return
	}
	token := firstNonEmpty(envelope.Token, envelope.Header.Token)
	if h.VerificationToken != "" && token != h.VerificationToken {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}
	if envelope.Type == "url_verification" {
		writeJSON(w, http.StatusOK, map[string]string{"challenge": envelope.Challenge})
		return
	}
	if strings.Contains(envelope.Header.EventType, "card.action") || envelope.Event.Action.Value != nil {
		actorID := firstNonEmpty(envelope.Event.Operator.OpenID, envelope.Event.Operator.UserID)
		if err := h.handleAction(r.Context(), envelope.Event.Action.Value, actorID); err != nil {
			h.Logger.Error("Feishu card action failed", "err", err)
			http.Error(w, "action failed", http.StatusBadRequest)
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	if envelope.Header.EventType != "" && envelope.Header.EventType != "im.message.receive_v1" {
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}
	event, err := DecodeMessageEvent(data)
	if err != nil || event.MessageID == "" || event.Text == "" {
		writeJSON(w, http.StatusOK, map[string]bool{"ignored": true})
		return
	}
	if h.Backend != nil {
		claimed, claimErr := h.Backend.ProcessedMessages().Claim(r.Context(), event.MessageID)
		if claimErr != nil {
			h.Logger.Error("Feishu duplicate guard failed", "messageId", event.MessageID, "err", claimErr)
			http.Error(w, "temporary failure", http.StatusInternalServerError)
			return
		}
		if !claimed {
			writeJSON(w, http.StatusOK, map[string]bool{"duplicate": true})
			return
		}
	}
	if h.Agent == nil {
		if h.Backend != nil {
			_ = h.Backend.ProcessedMessages().Release(r.Context(), event.MessageID)
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
	request := agent.NewRequest(agent.ChatScenario, event.Text,
		agent.WithRequestID(event.MessageID),
		agent.WithIdentity(event.UserID, event.ChatID, event.ChatType),
		agent.WithConversation(conversationID, rootID, event.MessageID),
		agent.WithListener(listener),
	)
	if err := h.Agent.Fire(request); err != nil {
		if h.Backend != nil {
			_ = h.Backend.ProcessedMessages().Release(r.Context(), event.MessageID)
		}
		h.Logger.Error("Feishu agent request rejected", "messageId", event.MessageID, "err", err)
		http.Error(w, "agent unavailable", http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"accepted": true})
}

func (h *Handler) responseListener(event MessageEvent, releaseClaim bool) agent.ResponseListener {
	var content string
	var runErr error
	return agent.ListenerFuncs{
		OnStartF: func(registry *agent.RunRegistry) {
			if h.Client != nil && h.Backend != nil {
				registry.AddQuestionHandler(&questionHandler{handler: h, event: event})
			}
		},
		OnContentF: func(value string) { content = value },
		OnErrorF:   func(err error) { runErr = err },
		OnFinishedF: func(outcome agent.Outcome) {
			if releaseClaim && outcome != agent.OutcomeCompleted && h.Backend != nil && event.MessageID != "" {
				if err := h.Backend.ProcessedMessages().Release(context.Background(), event.MessageID); err != nil {
					h.Logger.Error("Feishu duplicate claim release failed", "messageId", event.MessageID, "err", err)
				}
			}
			if h.Client == nil || event.ChatID == "" {
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
			if _, err := h.Client.ReplyText(context.Background(), event.MessageID, content); err != nil {
				h.Logger.Error("Feishu reply failed", "messageId", event.MessageID, "err", err)
			}
		},
	}
}

type questionHandler struct {
	handler *Handler
	event   MessageEvent
}

func (q *questionHandler) Ask(ctx context.Context, questions []tools.Question) (map[string]string, error) {
	if q.handler.Client == nil || q.handler.Backend == nil {
		return nil, &tools.ErrNotAnswered{Message: "Questions could not be shown in Feishu."}
	}
	pendingID := randomID()
	card := questionCard(pendingID, questions)
	cardMessageID, err := q.handler.Client.SendCard(ctx, ReceiveIDChatID, q.event.ChatID, card)
	if err != nil {
		return nil, err
	}
	questionsJSON, err := json.Marshal(questions)
	if err != nil {
		return nil, err
	}
	ttl := q.handler.QuestionTTL
	if ttl <= 0 || ttl > 14*24*time.Hour {
		ttl = 14 * 24 * time.Hour
	}
	now := q.handler.now()
	question := dao.PendingQuestion{ID: pendingID, UserID: q.event.UserID, ChatID: q.event.ChatID, ChatType: q.event.ChatType, ConversationID: firstNonEmpty(q.event.RootMessageID, q.event.ChatID, q.event.MessageID), RootMessageID: firstNonEmpty(q.event.RootMessageID, q.event.MessageID), CardID: cardMessageID, QuestionsJSON: string(questionsJSON), Status: dao.PendingQuestionStatusPending, CreatedAt: now, ExpiresAt: now.Add(ttl)}
	if err := q.handler.Backend.PendingQuestions().Save(ctx, question); err != nil {
		return nil, err
	}
	return map[string]string{}, nil
}

func (h *Handler) handleAction(ctx context.Context, value map[string]any, actorID string) error {
	if h.Backend == nil {
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
	pending, err := h.Backend.PendingQuestions().FindByID(ctx, pendingID)
	if err != nil {
		return err
	}
	if pending == nil || pending.Status != dao.PendingQuestionStatusPending {
		return errors.New("feishu: pending question is no longer answerable")
	}
	if actorID != "" && pending.UserID != "" && actorID != pending.UserID {
		return errors.New("feishu: card action is from a different user")
	}
	if !pending.ExpiresAt.IsZero() && !pending.ExpiresAt.After(h.now()) {
		return h.Backend.PendingQuestions().UpdateStatus(ctx, pending.ID, dao.PendingQuestionStatusExpired)
	}
	if err := h.Backend.PendingQuestions().UpdateStatus(ctx, pending.ID, dao.PendingQuestionStatusAnswered); err != nil {
		return err
	}
	if h.Agent == nil {
		_ = h.Backend.PendingQuestions().UpdateStatus(ctx, pending.ID, dao.PendingQuestionStatusPending)
		return errors.New("feishu: agent is not configured")
	}
	text := fmt.Sprintf("The user answered the question %q with: %s", question, answer)
	err = h.Agent.Fire(agent.NewRequest(agent.ChatScenario, text,
		agent.WithRequestID(pending.ID+":answer"),
		agent.WithIdentity(pending.UserID, pending.ChatID, pending.ChatType),
		agent.WithConversation(pending.ConversationID, pending.RootMessageID, pending.CardID),
		agent.WithListener(h.responseListener(MessageEvent{MessageID: pending.CardID, UserID: pending.UserID, ChatID: pending.ChatID, ChatType: pending.ChatType, RootMessageID: pending.RootMessageID}, false)),
	))
	if err != nil {
		_ = h.Backend.PendingQuestions().UpdateStatus(ctx, pending.ID, dao.PendingQuestionStatusPending)
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
	if h.Now != nil {
		return h.Now()
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

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

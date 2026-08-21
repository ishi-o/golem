package agent

import (
	"context"

	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/tools"
)

// Request describes one run. Built with NewRequest plus options; the zero
// Request is not runnable. Scenario and Text are required, and a caller that
// has no conversation id gets a memoryless run rather than an error: the
// identity fields default, ConversationID does not.
type Request struct {
	// RequestID is what Cancel stops; empty means not cancellable.
	RequestID string
	// Scenario is required (NewRequest's argument).
	Scenario Scenario
	UserID   string
	ChatID   string
	// ChatType defaults to "p2p".
	ChatType string
	// ConversationID groups the runs that share chat memory.
	ConversationID string
	RootMessageID  string
	ReplyMessageID string
	// Background runs post no answer of their own (see ScheduledTask).
	Background bool
	// PromptVariables extend the system prompt's variable set.
	PromptVariables map[string]string
	// Text is the user message. Images may accompany it.
	Images []Image
	// Listeners observe this run, alongside any the wiring declared.
	Listeners []ResponseListener
	// TodoHandlers receive this run's todo updates.
	TodoHandlers []tools.TodoEventHandler

	// UserText is set by the Text option; Message overrides it wholesale.
	UserText string
}

// Option mutates a Request during NewRequest.
type Option func(*Request)

// NewRequest starts a Request for a scenario. Required: scenario and text.
func NewRequest(scenario Scenario, text string, opts ...Option) Request {
	r := Request{Scenario: scenario, UserText: text, ChatType: "p2p"}
	for _, o := range opts {
		o(&r)
	}
	if r.ChatType == "" {
		r.ChatType = "p2p"
	}
	return r
}

// WithRequestID names the run for Cancel.
func WithRequestID(id string) Option { return func(r *Request) { r.RequestID = id } }

// WithIdentity sets who is talking and where.
func WithIdentity(userID, chatID, chatType string) Option {
	return func(r *Request) {
		r.UserID, r.ChatID, r.ChatType = userID, chatID, chatType
	}
}

// WithConversation places the run in a conversation (chat memory) and
// identifies the root and reply messages for surfaces.
func WithConversation(conversationID, rootMessageID, replyMessageID string) Option {
	return func(r *Request) {
		r.ConversationID, r.RootMessageID, r.ReplyMessageID = conversationID, rootMessageID, replyMessageID
	}
}

// WithBackground marks the run unattended (no answer of its own).
func WithBackground(background bool) Option { return func(r *Request) { r.Background = background } }

// WithPromptVariables supplies extra system-prompt variables.
func WithPromptVariables(vars map[string]string) Option {
	return func(r *Request) { r.PromptVariables = vars }
}

// WithImages attaches images to the user message, as URLs or data-URI
// Base64 payloads.
func WithImages(images ...Image) Option {
	return func(r *Request) { r.Images = append(r.Images, images...) }
}

// Image is one image attached to a user message: a URL, or Base64 data.
type Image struct {
	URL    string
	Base64 string
}

func (i Image) part() schema.ChatMessagePart {
	url := i.URL
	if url == "" && i.Base64 != "" {
		url = "data:image/png;base64," + i.Base64
	}
	return schema.ChatMessagePart{
		Type:     schema.ChatMessagePartTypeImageURL,
		ImageURL: &schema.ChatMessageImageURL{URL: url},
	}
}

// WithListener attaches a run listener.
func WithListener(l ResponseListener) Option {
	return func(r *Request) { r.Listeners = append(r.Listeners, l) }
}

// WithTodoHandler attaches a todo handler.
func WithTodoHandler(h tools.TodoEventHandler) Option {
	return func(r *Request) { r.TodoHandlers = append(r.TodoHandlers, h) }
}

// UserMessage assembles the user message for the model call.
func (r Request) UserMessage() *schema.Message {
	msg := &schema.Message{Role: schema.User, Content: r.UserText}
	if len(r.Images) > 0 {
		// MultiContent replaces Content for multimodal models; the text is
		// carried as the first part so nothing is lost.
		if r.UserText != "" {
			msg.MultiContent = append(msg.MultiContent, schema.ChatMessagePart{
				Type: schema.ChatMessagePartTypeText, Text: r.UserText,
			})
		}
		for _, img := range r.Images {
			msg.MultiContent = append(msg.MultiContent, img.part())
		}
	}
	return msg
}

// context builds the run's base context — the identity keys the agent
// forces after the request's own values, so a surface cannot mis-state who
// is talking.
func (r Request) context(ctx context.Context, mutators []func(context.Context) context.Context) context.Context {
	for _, m := range mutators {
		if m != nil {
			ctx = m(ctx)
		}
	}
	ctx = tools.UserID.With(ctx, r.UserID)
	ctx = tools.ChatID.With(ctx, r.ChatID)
	ctx = tools.ChatType.With(ctx, r.ChatType)
	ctx = tools.RootMessageID.With(ctx, r.RootMessageID)
	ctx = tools.ReplyMessageID.With(ctx, r.ReplyMessageID)
	return ctx
}

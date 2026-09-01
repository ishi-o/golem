package agent

import (
	"context"

	"github.com/cloudwego/eino/schema"

	"github.com/ishi-o/golem/core/knowledge"
	"github.com/ishi-o/golem/core/tools"
)

// Request describes one run. Built with NewRequest plus options; the zero
// Request is not runnable. Scenario and Text are required, and a caller that
// has no conversation id gets a memoryless run rather than an error: the
// identity fields default, ConversationID does not.
type Request struct {
	// RequestID is what Cancel stops; empty means not cancellable.
	RequestID string
	// ParentRequestID names the run that started this one, when it is a
	// subagent: cancelling the parent cancels this run, its tokens count on
	// the parent's turn, and the parent is held open until it finishes. A
	// parent that already ended is forgotten — there is nobody to tell.
	ParentRequestID string
	// Description is one line saying what the run is for, shown to listeners
	// of the parent run while a subagent works.
	Description string
	// Scenario is required (NewRequest's argument).
	Scenario Scenario
	UserID   string
	ChatID   string
	// ChatType defaults to "p2p".
	ChatType string
	// GroupID and TenantID scope the run's reads beyond the user's own
	// home: a group's or tenant's shared files and skills are read through
	// (never written to), and the knowledge base and sandbox carry the same
	// scope. Empty means no scope.
	GroupID  string
	TenantID string
	// SituationID is set by optional external-event triage runs. It is carried
	// to situation management tools and otherwise has no runtime meaning.
	SituationID string
	// ScheduledTaskID identifies the task whose firing is executing.
	ScheduledTaskID string
	// ConversationID groups the runs that share chat memory.
	ConversationID string
	RootMessageID  string
	ReplyMessageID string
	// Background runs post no answer of their own (see ScheduledTask).
	Background bool
	// PromptVariables extend the system prompt's variable set.
	PromptVariables map[string]string
	// Images may accompany Text.
	Images []Image
	// Listeners observe this run, alongside any application-level observers.
	Listeners []ResponseListener
	// TodoHandlers receive this run's todo updates.
	TodoHandlers []tools.TodoEventHandler

	// Text is the user message passed to the model.
	Text string
	// KnowledgeRetrieval is an explicit, fixed-scope lookup for unattended
	// runs. When nil, an attached knowledge base derives scope from identity and
	// retrieves using Text.
	KnowledgeRetrieval *knowledge.KnowledgeRetrieval
}

// RequestOption mutates a Request during NewRequest.
type RequestOption func(*Request)

// NewRequest starts a Request for a scenario. Required: scenario and text.
func NewRequest(scenario Scenario, text string, opts ...RequestOption) Request {
	r := Request{Scenario: scenario, Text: text, ChatType: "p2p"}
	for _, o := range opts {
		o(&r)
	}
	if r.ChatType == "" {
		r.ChatType = "p2p"
	}
	return r
}

// WithRequestID names the run for Cancel.
func WithRequestID(id string) RequestOption { return func(r *Request) { r.RequestID = id } }

// WithParent ties the run to the run that started it, making it a subagent:
// cancelled with the parent, its usage counted on the parent's turn, and the
// parent held open until it finishes.
func WithParent(parentRequestID string) RequestOption {
	return func(r *Request) { r.ParentRequestID = parentRequestID }
}

// WithDescription says what the run is for, in one line, where a surface
// shows work in progress.
func WithDescription(description string) RequestOption {
	return func(r *Request) { r.Description = description }
}

// WithIdentity sets who is talking and where.
func WithIdentity(userID, chatID, chatType string) RequestOption {
	return func(r *Request) {
		r.UserID, r.ChatID, r.ChatType = userID, chatID, chatType
	}
}

// WithScope gives the run a group and tenant: their homes' files and skills
// are read through the user's own, and the knowledge base and sandbox carry
// the same scope. Blank ids mean no scope.
func WithScope(groupID, tenantID string) RequestOption {
	return func(r *Request) {
		r.GroupID, r.TenantID = groupID, tenantID
	}
}

// WithSituationID binds optional event-management tools to one situation.
func WithSituationID(id string) RequestOption { return func(r *Request) { r.SituationID = id } }

// WithScheduledTaskID binds optional task self-control tools to a firing.
func WithScheduledTaskID(id string) RequestOption { return func(r *Request) { r.ScheduledTaskID = id } }

// WithKnowledgeRetrieval sets a fixed-scope retrieval request. The scope is
// not derived from the incoming message, which is important for event bodies
// and other untrusted briefings.
func WithKnowledgeRetrieval(retrieval knowledge.KnowledgeRetrieval) RequestOption {
	return func(r *Request) { r.KnowledgeRetrieval = &retrieval }
}

// WithConversation places the run in a conversation (chat memory) and
// identifies the root and reply messages for surfaces.
func WithConversation(conversationID, rootMessageID, replyMessageID string) RequestOption {
	return func(r *Request) {
		r.ConversationID, r.RootMessageID, r.ReplyMessageID = conversationID, rootMessageID, replyMessageID
	}
}

// WithBackground marks the run unattended (no answer of its own).
func WithBackground(background bool) RequestOption {
	return func(r *Request) { r.Background = background }
}

// WithPromptVariables supplies extra system-prompt variables.
func WithPromptVariables(vars map[string]string) RequestOption {
	return func(r *Request) { r.PromptVariables = vars }
}

// WithImages attaches images to the user message, as URLs or data-URI
// Base64 payloads.
func WithImages(images ...Image) RequestOption {
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
func WithListener(l ResponseListener) RequestOption {
	return func(r *Request) { r.Listeners = append(r.Listeners, l) }
}

// WithTodoHandler attaches a todo handler.
func WithTodoHandler(h tools.TodoEventHandler) RequestOption {
	return func(r *Request) { r.TodoHandlers = append(r.TodoHandlers, h) }
}

// UserMessage assembles the user message for the model call.
func (r Request) UserMessage() *schema.Message {
	msg := &schema.Message{Role: schema.User, Content: r.Text}
	if len(r.Images) > 0 {
		// MultiContent replaces Content for multimodal models; the text is
		// carried as the first part so nothing is lost.
		if r.Text != "" {
			msg.MultiContent = append(msg.MultiContent, schema.ChatMessagePart{
				Type: schema.ChatMessagePartTypeText, Text: r.Text,
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
	ctx = tools.ConversationID.With(ctx, r.ConversationID)
	ctx = tools.RequestID.With(ctx, r.RequestID)
	ctx = tools.GroupID.With(ctx, r.GroupID)
	ctx = tools.TenantID.With(ctx, r.TenantID)
	ctx = tools.SituationID.With(ctx, r.SituationID)
	ctx = tools.ScheduledTaskID.With(ctx, r.ScheduledTaskID)
	return ctx
}

// Package tools is the tool subsystem: the per-request context values tools
// read, the interceptor chain, the composition that assembles a run's tool
// set, and the built-in tools.
//
// spring-agent threaded identity to tools through Spring AI's ToolContext —
// a map carried alongside the model call. eino's InvokableRun takes a plain
// context.Context, so the Go port carries identity the Go way: as typed
// context values, assembled once by the agent before the run starts. A tool
// reads them with tools.UserID.Get(ctx), never by string key — the typed key
// is what keeps a stringly identity bug from compiling.
package tools

import "context"

// Key is a typed context key. The zero Key is not usable; construct with
// NewKey, package-level, next to the type it names.
type Key[T any] struct{ name string }

// NewKey names a typed context key. The name appears in error messages; it
// does not have to be unique across types, but a collision on (name, type)
// is a silent aliasing bug, so prefer one key per name.
func NewKey[T any](name string) Key[T] { return Key[T]{name: name} }

type holder struct {
	name  string
	value any
}

// With returns a context carrying value under this key.
func (k Key[T]) With(ctx context.Context, value T) context.Context {
	return context.WithValue(ctx, holder{}, holder{name: k.name, value: value})
}

// Get reads the value. A value stored under the same name with a different
// type — a registration mistake, not a runtime condition — is an error
// rather than a silent zero.
func (k Key[T]) Get(ctx context.Context) (T, error) {
	var zero T
	h, ok := ctx.Value(holder{}).(holder)
	if !ok || h.name != k.name {
		return zero, nil
	}
	v, ok := h.value.(T)
	if !ok {
		return zero, &TypeMismatchError{key: k.name, want: h.value}
	}
	return v, nil
}

// Require reads the value and fails when it is absent or the string form is
// blank — for the identities a tool cannot do its job without.
func (k Key[T]) Require(ctx context.Context) (T, error) {
	v, err := k.Get(ctx)
	if err != nil {
		return v, err
	}
	if s, is := any(v).(string); is && s == "" {
		return v, &MissingError{key: k.name}
	}
	return v, nil
}

// TypeMismatchError reports a context value stored under a name with an
// unexpected type.
type TypeMismatchError struct {
	key  string
	want any
}

func (e *TypeMismatchError) Error() string {
	return "tool context value " + e.key + " has unexpected type " + typeName(e.want)
}

// MissingError reports a required context value that is absent or blank.
type MissingError struct{ key string }

func (e *MissingError) Error() string { return "tool context value " + e.key + " is required" }

// The identity keys the runtime forces onto every run, after the request's
// own values, so a surface cannot mis-state who is talking. The names match
// spring-agent's ToolContexts keys.
var (
	// UserID is the channel identity of the user whose words started the run.
	UserID = NewKey[string]("userId")
	// ChatID is the conversation the message arrived in.
	ChatID = NewKey[string]("chatId")
	// ChatType is "p2p" or "group".
	ChatType = NewKey[string]("chatType")
	// RootMessageID is the message a thread is rooted at.
	RootMessageID = NewKey[string]("rootMessageId")
	// ReplyMessageID is the message this run's reply will answer.
	ReplyMessageID = NewKey[string]("replyMessageId")
)

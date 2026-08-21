// Package tools is the tool subsystem: the per-request context values tools
// read, the interceptor chain, the composition that assembles a run's tool
// set, and the built-in tools.
//
// Eino tools receive a plain context.Context, so the runtime carries identity
// as typed context values, assembled once by the agent before the run starts.
// A tool reads them with tools.UserID.Get(ctx), never by string key.
package tools

import "context"

// Key is a typed context key. The zero Key is not usable; construct with
// NewKey, package-level, next to the type it names.
type Key[T any] struct {
	name string
	id   *keyID
}

// keyID is deliberately allocated once per key. Using a single empty struct
// as the context key would make every identity value overwrite every other
// one, because context compares keys by equality rather than by their
// generic type parameter.
type keyID struct{}

// NewKey names a typed context key. Each call creates an independent key; the
// name appears only in error messages and should still describe one concept.
func NewKey[T any](name string) Key[T] { return Key[T]{name: name, id: &keyID{}} }

// With returns a context carrying value under this key.
func (k Key[T]) With(ctx context.Context, value T) context.Context {
	if k.id == nil {
		return ctx
	}
	return context.WithValue(ctx, k.id, value)
}

// Get reads the value. A missing value is reported as a zero value and nil
// error; Require is the convenience for tools that need a non-empty value.
func (k Key[T]) Get(ctx context.Context) (T, error) {
	var zero T
	if k.id == nil {
		return zero, nil
	}
	v, ok := ctx.Value(k.id).(T)
	if !ok {
		return zero, nil
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
// The names are also used in logs and diagnostics.
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

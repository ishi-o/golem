package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

// Interceptor is cross-cutting behaviour around tool calls, applied by
// wrapping every tool the run offers — the Go shape of spring-agent's
// ToolCallInterceptor. BeforeCall sees the arguments the model produced and
// may rewrite them; AfterCall sees the result and may replace it (the large
// response interceptor diverts to a file and returns a pointer instead).
//
// An interceptor may also end the turn: EndTurn in the AfterCall result
// stops the run after this tool returns, the equivalent of Spring AI's
// returnDirect metadata — the ask tool uses it when answers can only arrive
// after the run is over.
type Interceptor interface {
	BeforeCall(ctx context.Context, name string, arguments string) (string, error)
	AfterCall(ctx context.Context, name string, arguments string, result string) (string, bool, error)
}

// InterceptorFuncs adapts functions to Interceptor, for interceptors that
// only need one of the two hooks.
type InterceptorFuncs struct {
	Before func(ctx context.Context, name string, arguments string) (string, error)
	After  func(ctx context.Context, name string, arguments string, result string) (string, bool, error)
}

func (f InterceptorFuncs) BeforeCall(ctx context.Context, name, arguments string) (string, error) {
	if f.Before == nil {
		return arguments, nil
	}
	return f.Before(ctx, name, arguments)
}

func (f InterceptorFuncs) AfterCall(ctx context.Context, name, arguments, result string) (string, bool, error) {
	if f.After == nil {
		return result, false, nil
	}
	return f.After(ctx, name, arguments, result)
}

// intercepted wraps an InvokableTool with the interceptor chain. It
// forwards Info untouched — the metadata the model sees must be the wrapped
// tool's own, or a tool whose result ends the turn would silently become one
// whose result does not (the exact bug spring-agent's InterceptingToolCallback
// comment records).
type intercepted struct {
	delegate     tool.InvokableTool
	interceptors []Interceptor
}

// WrapTool applies interceptors to a tool. Order matters and is documented:
// BeforeCall runs outermost-first, AfterCall innermost-first, so an
// interceptor sees the result in the order it saw the arguments.
func WrapTool(t tool.InvokableTool, interceptors ...Interceptor) tool.InvokableTool {
	if len(interceptors) == 0 {
		return t
	}
	return &intercepted{delegate: t, interceptors: interceptors}
}

func (w *intercepted) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return w.delegate.Info(ctx)
}

func (w *intercepted) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	args := argumentsInJSON
	for _, i := range w.interceptors {
		var err error
		args, err = i.BeforeCall(ctx, toolName(ctx, w), args)
		if err != nil {
			return "", fmt.Errorf("interceptor before %s: %w", toolName(ctx, w), err)
		}
	}
	result, err := w.delegate.InvokableRun(ctx, args, opts...)
	if err != nil {
		return "", err
	}
	endTurn := false
	for j := len(w.interceptors) - 1; j >= 0; j-- {
		var stop bool
		var ierr error
		result, stop, ierr = w.interceptors[j].AfterCall(ctx, toolName(ctx, w), args, result)
		if ierr != nil {
			return "", fmt.Errorf("interceptor after %s: %w", toolName(ctx, w), ierr)
		}
		endTurn = endTurn || stop
	}
	if endTurn {
		// The sentinel is private to this package; the agent's tool executor
		// unwraps it. Travelling inside the result string (rather than a
		// second return value) keeps the wrapped tool's signature the
		// framework's own.
		return endTurnPrefix + result, nil
	}
	return result, nil
}

// endTurnPrefix marks a tool result whose run should stop after this call.
// A result that already starts with it (a tool echoing a malicious file's
// contents, say) cannot fake the signal: the prefix is stripped wherever it
// appears, so the model never sees it.
const endTurnPrefix = "\x00golem:end-turn\x00"

// SplitEndTurn reports whether a tool result carries the end-turn sentinel,
// returning the result without it. InferTool JSON-encodes a string result, so
// this accepts both the raw form produced by a custom tool and the JSON string
// form produced by Eino's typed tool helper.
func SplitEndTurn(result string) (string, bool) {
	if strings.HasPrefix(result, endTurnPrefix) {
		return result[len(endTurnPrefix):], true
	}
	var decoded string
	if json.Unmarshal([]byte(result), &decoded) == nil && strings.HasPrefix(decoded, endTurnPrefix) {
		return decoded[len(endTurnPrefix):], true
	}
	return result, false
}

// toolName resolves the wrapped tool's name for log and error messages. The
// Info call is cheap (the built-ins return a stored value) and keeps the
// wrapper honest about what it is delegating to.
func toolName(ctx context.Context, w *intercepted) string {
	info, err := w.delegate.Info(ctx)
	if err != nil || info == nil {
		return "?"
	}
	return info.Name
}

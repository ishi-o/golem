package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

// Interceptor is cross-cutting behavior around tool calls. BeforeCall sees
// the arguments the model produced and may rewrite them; AfterCall sees the
// result and may replace it.
//
// An interceptor may also end the turn: EndTurn in the AfterCall result
// stops the run after this tool returns. The ask tool uses it when answers can
// only arrive after the run is over.
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

// interceptorMiddleware adapts golem's interceptor contract to Eino's tool
// middleware. The provider uses this path so ToolsNode owns tool dispatch and
// message construction while golem keeps its argument-rewrite and end-turn
// policies.
func interceptorMiddleware(interceptors []Interceptor) compose.ToolMiddleware {
	return interceptorMiddlewareWithErrors(interceptors, true)
}

func interceptorMiddlewareWithErrors(interceptors []Interceptor, recoverErrors bool) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				arguments := input.Arguments
				for _, interceptor := range interceptors {
					var err error
					arguments, err = interceptor.BeforeCall(ctx, input.Name, arguments)
					if err != nil {
						return finishToolCall(ctx, recoverErrors, fmt.Errorf("interceptor before %s: %w", input.Name, err))
					}
				}

				call := *input
				call.Arguments = arguments
				output, err := next(ctx, &call)
				if err != nil {
					return finishToolCall(ctx, recoverErrors, err)
				}
				if output == nil {
					return finishToolCall(ctx, recoverErrors, fmt.Errorf("tool %s returned nil output", input.Name))
				}

				result := output.Result
				endTurn := false
				for j := len(interceptors) - 1; j >= 0; j-- {
					var stop bool
					result, stop, err = interceptors[j].AfterCall(ctx, input.Name, arguments, result)
					if err != nil {
						return finishToolCall(ctx, recoverErrors, fmt.Errorf("interceptor after %s: %w", input.Name, err))
					}
					endTurn = endTurn || stop
				}
				if endTurn {
					result = endTurnPrefix + result
				}
				return &compose.ToolOutput{Result: result}, nil
			}
		},
	}
}

func finishToolCall(ctx context.Context, recoverErrors bool, err error) (*compose.ToolOutput, error) {
	if !recoverErrors || ctx.Err() != nil {
		return nil, err
	}
	return &compose.ToolOutput{Result: fmt.Sprintf("tool error: %v", err)}, nil
}

// intercepted is the compatibility adapter returned by WrapTool. Provider
// runs use interceptorMiddleware directly; keeping this small adapter avoids
// breaking callers that used WrapTool outside a Provider.
type intercepted struct {
	delegate tool.InvokableTool
	endpoint compose.InvokableToolEndpoint
}

// WrapTool applies interceptors to a tool. Order matters and is documented:
// BeforeCall runs outermost-first, AfterCall innermost-first, so an
// interceptor sees the result in the order it saw the arguments.
//
// Deprecated: register the interceptor with NewProvider instead. Provider
// composition uses Eino's compose.ToolsNode middleware directly.
func WrapTool(t tool.InvokableTool, interceptors ...Interceptor) tool.InvokableTool {
	if len(interceptors) == 0 {
		return t
	}
	endpoint := interceptorMiddlewareWithErrors(interceptors, false).Invokable(func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
		result, err := t.InvokableRun(ctx, input.Arguments, input.CallOptions...)
		if err != nil {
			return nil, err
		}
		return &compose.ToolOutput{Result: result}, nil
	})
	return &intercepted{delegate: t, endpoint: endpoint}
}

func (w *intercepted) Info(ctx context.Context) (*schema.ToolInfo, error) {
	return w.delegate.Info(ctx)
}

func (w *intercepted) InvokableRun(ctx context.Context, argumentsInJSON string, opts ...tool.Option) (string, error) {
	output, err := w.endpoint(ctx, &compose.ToolInput{
		Name:        toolName(ctx, w),
		Arguments:   argumentsInJSON,
		CallOptions: opts,
	})
	if err != nil {
		return "", err
	}
	if output == nil {
		return "", fmt.Errorf("tool %s returned nil output", toolName(ctx, w))
	}
	return output.Result, nil
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

// toolName resolves the wrapped tool's name for compatibility calls.
func toolName(ctx context.Context, w *intercepted) string {
	info, err := w.delegate.Info(ctx)
	if err != nil || info == nil {
		return "?"
	}
	return info.Name
}

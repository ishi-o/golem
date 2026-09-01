package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/cloudwego/eino/compose"
)

// Interceptor is the convenient before/after facade for ordinary invokable
// tool calls. BeforeCall can rewrite the model's arguments; AfterCall can
// rewrite the result or request that the run end after the call.
//
// Interceptors are adapted to Eino middleware by WithInterceptor. Use
// WithToolMiddleware directly when the full Eino hook surface is required.
type Interceptor interface {
	BeforeCall(ctx context.Context, name string, arguments string) (string, error)
	AfterCall(ctx context.Context, name string, arguments string, result string) (string, bool, error)
}

// InterceptorFuncs adapts functions to Interceptor for hooks that only need
// one stage.
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

// interceptorMiddleware adapts one convenience interceptor to Eino's native
// middleware contract. Tool dispatch remains in compose.ToolsNode; this only
// translates the before/after hooks and the golem end-turn marker.
func interceptorMiddleware(interceptor Interceptor) compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				arguments, err := interceptor.BeforeCall(ctx, input.Name, input.Arguments)
				if err != nil {
					return nil, fmt.Errorf("interceptor before %s: %w", input.Name, err)
				}

				call := *input
				call.Arguments = arguments
				output, err := next(ctx, &call)
				if err != nil {
					return nil, err
				}
				if output == nil {
					return nil, fmt.Errorf("tool %s returned nil output", input.Name)
				}

				result, endTurn, err := interceptor.AfterCall(ctx, input.Name, arguments, output.Result)
				if err != nil {
					return nil, fmt.Errorf("interceptor after %s: %w", input.Name, err)
				}
				if endTurn {
					result = EndTurnResult(result)
				}
				return &compose.ToolOutput{Result: result}, nil
			}
		},
	}
}

// toolErrorMiddleware turns ordinary tool failures into model-visible tool
// results. Cancellation still propagates so the agent can classify it as a
// cancelled run. It is outermost in the provider's chain and therefore also
// covers errors returned by caller-supplied middleware.
func toolErrorMiddleware() compose.ToolMiddleware {
	return compose.ToolMiddleware{
		Invokable: func(next compose.InvokableToolEndpoint) compose.InvokableToolEndpoint {
			return func(ctx context.Context, input *compose.ToolInput) (*compose.ToolOutput, error) {
				output, err := next(ctx, input)
				if err != nil {
					return recoverToolError(ctx, err)
				}
				if output == nil {
					return recoverToolError(ctx, fmt.Errorf("tool %s returned nil output", input.Name))
				}
				return output, nil
			}
		},
	}
}

func recoverToolError(ctx context.Context, err error) (*compose.ToolOutput, error) {
	if ctx.Err() != nil {
		return nil, err
	}
	return &compose.ToolOutput{Result: fmt.Sprintf("tool error: %v", err)}, nil
}

// endTurnPrefix marks a tool result whose run should stop after this call.
// A result that already starts with it (a tool echoing a malicious file's
// contents, say) cannot fake the signal: the prefix is stripped wherever it
// appears, so the model never sees it.
const endTurnPrefix = "\x00golem:end-turn\x00"

// EndTurnResult marks a tool result so the current run ends after the tool
// call. Use it from native Eino tool middleware when a tool needs to hand
// work to a later run, such as a question whose answer arrives asynchronously.
func EndTurnResult(result string) string {
	return endTurnPrefix + result
}

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

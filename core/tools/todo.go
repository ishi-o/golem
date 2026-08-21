package tools

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// TodoStatus is one todo item's state.
type TodoStatus string

const (
	TodoPending    TodoStatus = "pending"
	TodoInProgress TodoStatus = "in_progress"
	TodoCompleted  TodoStatus = "completed"
)

// TodoEvent is one TodoWrite call, delivered whole to the run's todo
// handlers — a surface renders the list, it does not diff it, because the
// model's contract is "rewrite the list to what is true now".
type TodoEvent struct {
	Todos []Todo
}

// Todo is one item of the list.
type Todo struct {
	Content string
	Status  TodoStatus
}

// TodoEventHandler receives todo updates during a run.
type TodoEventHandler func(ctx context.Context, event TodoEvent)

// TodoFanOut combines handlers into one: every handler hears every event,
// and no handler's failure (or panic) reaches the tool call that produced
// the event.
func TodoFanOut(handlers []TodoEventHandler) TodoEventHandler {
	if len(handlers) == 0 {
		return nil
	}
	return fanOutTodos(handlers)
}

// fanOutTodos delivers an event to every handler, one handler's failure
// costing only its own view: the card rendering the list may be gone (the
// run outlives surfaces that close early), and that must not fail the tool
// call the model made to record its plan.
func fanOutTodos(handlers []TodoEventHandler) TodoEventHandler {
	return func(ctx context.Context, event TodoEvent) {
		for _, h := range handlers {
			if h == nil {
				continue
			}
			// A panic in a renderer is contained the same way: the todo list
			// is presentation, never a dependency of the run.
			func() {
				defer func() {
					if r := recover(); r != nil {
						slog.Default().Error("todo handler panicked", "panic", r)
					}
				}()
				h(ctx, event)
			}()
		}
	}
}

// TodoWrite is the plan tool. The whole list is replaced on every call: a
// diffing protocol would make the model compute diffs, and the last call
// before the final answer is what leaves no item in progress.
func TodoWrite(handler TodoEventHandler) tool.InvokableTool {
	type item struct {
		Content string     `json:"content"`
		Status  TodoStatus `json:"status"`
	}
	return mustTool(utils.InferTool(ToolNameTodoWrite,
		"Record or update the plan for the current task as a list of todo items with status pending, in_progress or completed. Each call replaces the whole list; call it before multi-step work and update items as you go. No item may be left in_progress in the final call.",
		func(ctx context.Context, in struct {
			Todos []item `json:"todos"`
		}) (string, error) {
			event := TodoEvent{Todos: make([]Todo, 0, len(in.Todos))}
			var problems []string
			for i, t := range in.Todos {
				switch t.Status {
				case TodoPending, TodoInProgress, TodoCompleted:
				case "":
					t.Status = TodoPending
				default:
					problems = append(problems, fmt.Sprintf("todos[%d].status %q is not pending, in_progress or completed", i, t.Status))
					continue
				}
				event.Todos = append(event.Todos, Todo{Content: t.Content, Status: t.Status})
			}
			if len(problems) > 0 {
				return "", fmt.Errorf("%s", strings.Join(problems, "; "))
			}
			if handler != nil {
				handler(ctx, event)
			}
			return fmt.Sprintf("%d todos recorded", len(event.Todos)), nil
		}))
}

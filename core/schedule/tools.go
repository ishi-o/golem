package schedule

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/tools"
)

// Tools are the schedule tools: create, list, cancel. They are registered
// with the provider and excluded from ScheduledTaskScenario runs — a firing
// must not schedule more work.
type Tools struct {
	runner *Runner
	tasks  store.ScheduledTaskStore
}

// NewTools returns the tools over one runner and its task store, for
// provider registration.
func NewTools(r *Runner, tasks store.ScheduledTaskStore) *Tools {
	return &Tools{runner: r, tasks: tasks}
}

// List lists the schedule tools, satisfying tools.Builtin.
func (t *Tools) List() []tool.InvokableTool {
	return []tool.InvokableTool{t.create(), t.listTasks(), t.cancel()}
}

// RegisterBuiltins registers the schedule tools on the provider. The
// SCHEDULED_TASK scenario excludes them by name, so a firing cannot schedule
// more work.
func RegisterBuiltins(p *tools.Provider, t *Tools) {
	for _, tl := range t.List() {
		p.Register(tl, nil)
	}
}

func (t *Tools) create() tool.InvokableTool {
	type input struct {
		// TaskText is the prompt to run at firing time.
		TaskText string `json:"taskText"`
		// Cron is a 5-field cron expression; exactly one of Cron and
		// ScheduledAt must be set.
		Cron string `json:"cron,omitempty"`
		// ScheduledAt is an RFC 3339 instant for a one-shot task.
		ScheduledAt string `json:"scheduledAt,omitempty"`
		// Expiry is a duration ("168h") bounding the task's life; empty
		// means the default (7 days); "never" is explicit.
		Expiry string `json:"expiry,omitempty"`
	}
	return tools.MustTool(utils.InferTool(tools.ToolNameCreateScheduledTask,
		"Schedule a task to run later: a one-shot at a time (scheduledAt, RFC 3339) or a recurring cron (5-field: minute hour day-of-month month day-of-week). Do not schedule work as part of answering; agree it with the user first.",
		func(ctx context.Context, in input) (string, error) {
			userID, err := tools.UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			chatID, _ := tools.ChatID.Get(ctx)
			chatType, _ := tools.ChatType.Get(ctx)
			rootMessageID, _ := tools.RootMessageID.Get(ctx)

			if t.runner.cfg.Scheduler == nil {
				return "", fmt.Errorf("scheduled tasks are not configured in this deployment")
			}
			if (in.Cron == "") == (in.ScheduledAt == "") {
				return "", fmt.Errorf("set exactly one of cron and scheduledAt")
			}
			task := store.ScheduledTask{
				ID:            newTaskID(),
				UserID:        userID,
				ChatID:        chatID,
				ChatType:      chatType,
				RootMessageID: rootMessageID,
				TaskText:      in.TaskText,
				Status:        store.ScheduledTaskStatusActive,
			}
			if in.Cron != "" {
				// Verbatim: the injected scheduler is the expression's
				// validator (Runner.Schedule arms before persisting, so an
				// expression it rejects never becomes a task).
				task.CronExpression = in.Cron
			} else {
				at, err := time.Parse(time.RFC3339, in.ScheduledAt)
				if err != nil {
					return "", fmt.Errorf("scheduledAt %q is not RFC 3339: %v", in.ScheduledAt, err)
				}
				task.ScheduledAt = at
			}
			switch in.Expiry {
			case "", "never":
				if in.Expiry == "" {
					task.ExpiresAt = time.Now().Add(t.runner.cfg.DefaultExpiry)
				}
			default:
				d, err := time.ParseDuration(in.Expiry)
				if err != nil || d <= 0 {
					return "", fmt.Errorf("expiry %q is not a positive duration", in.Expiry)
				}
				task.ExpiresAt = time.Now().Add(d)
			}
			if err := t.runner.Schedule(task); err != nil {
				return "", err
			}
			return "scheduled task " + task.ID, nil
		}))
}

func (t *Tools) listTasks() tool.InvokableTool {
	type task struct {
		ID      string `json:"id"`
		Text    string `json:"text"`
		Cron    string `json:"cron,omitempty"`
		At      string `json:"at,omitempty"`
		Expires string `json:"expires,omitempty"`
	}
	type output struct {
		Tasks []task `json:"tasks"`
	}
	return tools.MustTool(utils.InferTool(tools.ToolNameListScheduledTasks,
		"List the user's active scheduled tasks.",
		func(ctx context.Context, _ struct{}) (output, error) {
			userID, err := tools.UserID.Require(ctx)
			if err != nil {
				return output{}, err
			}
			active, err := t.tasks.ListByUserAndStatus(ctx, userID, store.ScheduledTaskStatusActive)
			if err != nil {
				return output{}, err
			}
			out := output{}
			for _, st := range active {
				item := task{ID: st.ID, Text: st.TaskText, Cron: st.CronExpression}
				if !st.ScheduledAt.IsZero() {
					item.At = st.ScheduledAt.Format(time.RFC3339)
				}
				if !st.ExpiresAt.IsZero() {
					item.Expires = st.ExpiresAt.Format(time.RFC3339)
				}
				out.Tasks = append(out.Tasks, item)
			}
			return out, nil
		}))
}

func (t *Tools) cancel() tool.InvokableTool {
	return tools.MustTool(utils.InferTool(tools.ToolNameCancelScheduledTask,
		"Cancel one of the user's scheduled tasks by id. A firing in progress is stopped too.",
		func(ctx context.Context, in struct {
			TaskID string `json:"taskId"`
		},
		) (string, error) {
			userID, err := tools.UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			task, err := t.tasks.Get(ctx, in.TaskID)
			if err != nil || task == nil || task.UserID != userID {
				return "", fmt.Errorf("no scheduled task %s owned by you", in.TaskID)
			}
			t.runner.Unschedule(in.TaskID)
			if err := t.tasks.SetStatus(ctx, in.TaskID, store.ScheduledTaskStatusCancelled); err != nil {
				return "", err
			}
			return "cancelled task " + in.TaskID, nil
		}))
}

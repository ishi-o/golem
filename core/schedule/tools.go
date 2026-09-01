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

// Tools are the schedule tools: create, list, cancel, update, and task-local
// stop/reschedule. They are registered
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
	return []tool.InvokableTool{t.create(), t.listTasks(), t.cancel(), t.update(), t.stopThis(), t.rescheduleThis()}
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
		// MaxRuns bounds recurring firings; zero means unlimited.
		MaxRuns int `json:"maxRuns,omitempty"`
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
			conversationID, _ := tools.ConversationID.Get(ctx)
			groupID, _ := tools.GroupID.Get(ctx)
			tenantID, _ := tools.TenantID.Get(ctx)

			if t.runner.cfg.Scheduler == nil {
				return "", fmt.Errorf("scheduled tasks are not configured in this deployment")
			}
			if (in.Cron == "") == (in.ScheduledAt == "") {
				return "", fmt.Errorf("set exactly one of cron and scheduledAt")
			}
			task := store.ScheduledTask{
				ID:             newTaskID(),
				UserID:         userID,
				ChatID:         chatID,
				ChatType:       chatType,
				RootMessageID:  rootMessageID,
				ConversationID: conversationID,
				GroupID:        groupID,
				TenantID:       tenantID,
				TaskText:       in.TaskText,
				MaxRuns:        in.MaxRuns,
				Status:         store.ScheduledTaskStatusActive,
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
			if in.MaxRuns < 0 {
				return "", fmt.Errorf("maxRuns must not be negative")
			}
			switch in.Expiry {
			case "", "never":
				if in.Expiry == "" {
					task.ExpiresAt = t.runner.cfg.Clock().Add(t.runner.cfg.DefaultExpiry)
				}
			default:
				d, err := time.ParseDuration(in.Expiry)
				if err != nil || d <= 0 {
					return "", fmt.Errorf("expiry %q is not a positive duration", in.Expiry)
				}
				task.ExpiresAt = t.runner.cfg.Clock().Add(d)
			}
			if err := t.runner.Schedule(task); err != nil {
				return "", err
			}
			return "scheduled task " + task.ID, nil
		}))
}

func (t *Tools) listTasks() tool.InvokableTool {
	type task struct {
		ID             string `json:"id"`
		Text           string `json:"text"`
		Cron           string `json:"cron,omitempty"`
		At             string `json:"at,omitempty"`
		Expires        string `json:"expires,omitempty"`
		ConversationID string `json:"conversationId,omitempty"`
		MaxRuns        int    `json:"maxRuns,omitempty"`
		RunCount       int    `json:"runCount"`
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
				item := task{ID: st.ID, Text: st.TaskText, Cron: st.CronExpression, ConversationID: st.ConversationID, MaxRuns: st.MaxRuns, RunCount: st.RunCount}
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

func (t *Tools) update() tool.InvokableTool {
	type input struct {
		TaskID      string `json:"taskId"`
		TaskText    string `json:"taskText,omitempty"`
		Cron        string `json:"cron,omitempty"`
		ScheduledAt string `json:"scheduledAt,omitempty"`
		Expiry      string `json:"expiry,omitempty"`
		Background  *bool  `json:"background,omitempty"`
		MaxRuns     *int   `json:"maxRuns,omitempty"`
	}
	return tools.MustTool(utils.InferTool(tools.ToolNameUpdateScheduledTask,
		"Update a scheduled task owned by the current user. Keep either its cron or one-shot time, and only supplied fields are changed.",
		func(ctx context.Context, in input) (string, error) {
			userID, err := tools.UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			task, err := t.tasks.Get(ctx, in.TaskID)
			if err != nil || task == nil || task.UserID != userID {
				return "", fmt.Errorf("no scheduled task %s owned by you", in.TaskID)
			}
			if in.TaskText != "" {
				task.TaskText = in.TaskText
			}
			if in.Cron != "" && in.ScheduledAt != "" {
				return "", fmt.Errorf("set only one of cron and scheduledAt")
			}
			if in.Cron != "" {
				task.CronExpression, task.ScheduledAt, task.NextFireAt = in.Cron, time.Time{}, time.Time{}
			} else if in.ScheduledAt != "" {
				at, parseErr := time.Parse(time.RFC3339, in.ScheduledAt)
				if parseErr != nil {
					return "", fmt.Errorf("scheduledAt %q is not RFC 3339: %v", in.ScheduledAt, parseErr)
				}
				task.CronExpression, task.ScheduledAt, task.NextFireAt = "", at, at
			}
			if in.Background != nil {
				task.Background = *in.Background
			}
			if in.MaxRuns != nil {
				if *in.MaxRuns < 0 || (*in.MaxRuns > 0 && *in.MaxRuns < task.RunCount) {
					return "", fmt.Errorf("maxRuns must be zero or at least the existing run count")
				}
				task.MaxRuns = *in.MaxRuns
			}
			if in.Expiry != "" {
				if in.Expiry == "never" {
					task.ExpiresAt = time.Time{}
				} else {
					d, parseErr := time.ParseDuration(in.Expiry)
					if parseErr != nil || d <= 0 {
						return "", fmt.Errorf("expiry %q is not a positive duration", in.Expiry)
					}
					task.ExpiresAt = t.runner.cfg.Clock().Add(d)
				}
			}
			if err := t.runner.Update(ctx, *task); err != nil {
				return "", err
			}
			return "updated scheduled task " + in.TaskID, nil
		}))
}

func (t *Tools) stopThis() tool.InvokableTool {
	return tools.MustTool(utils.InferTool(tools.ToolNameStopScheduledTask,
		"Stop the scheduled task whose firing is currently running. Only available inside a scheduled task.",
		func(ctx context.Context, _ struct{}) (string, error) {
			id, err := tools.ScheduledTaskID.Require(ctx)
			if err != nil {
				return "", err
			}
			task, err := t.tasks.Get(ctx, id)
			if err != nil || task == nil {
				return "", fmt.Errorf("scheduled task %q was not found", id)
			}
			if task.Status != store.ScheduledTaskStatusActive {
				return "", fmt.Errorf("scheduled task %q is no longer active", id)
			}
			if err := t.runner.StopFiringTask(ctx, id); err != nil {
				return "", err
			}
			return "stopped scheduled task " + id, nil
		}))
}

func (t *Tools) rescheduleThis() tool.InvokableTool {
	type input struct {
		Cron        string `json:"cron,omitempty"`
		ScheduledAt string `json:"scheduledAt,omitempty"`
		TaskText    string `json:"taskText,omitempty"`
	}
	return tools.MustTool(utils.InferTool(tools.ToolNameRescheduleTask,
		"Change the next schedule of the task whose firing is currently running. Set exactly one of cron or scheduledAt.",
		func(ctx context.Context, in input) (string, error) {
			id, err := tools.ScheduledTaskID.Require(ctx)
			if err != nil {
				return "", err
			}
			if (in.Cron == "") == (in.ScheduledAt == "") {
				return "", fmt.Errorf("set exactly one of cron and scheduledAt")
			}
			task, err := t.tasks.Get(ctx, id)
			if err != nil || task == nil {
				return "", fmt.Errorf("scheduled task %q was not found", id)
			}
			if task.Status != store.ScheduledTaskStatusActive {
				return "", fmt.Errorf("scheduled task %q is no longer active", id)
			}
			if task.CronExpression != "" {
				return "", fmt.Errorf("recurring scheduled tasks cannot be rescheduled; stop the task instead")
			}
			if in.Cron != "" {
				task.CronExpression, task.ScheduledAt, task.NextFireAt = in.Cron, time.Time{}, time.Time{}
			} else {
				at, parseErr := time.Parse(time.RFC3339, in.ScheduledAt)
				if parseErr != nil {
					return "", fmt.Errorf("scheduledAt %q is not RFC 3339: %v", in.ScheduledAt, parseErr)
				}
				if !at.After(t.runner.cfg.Clock()) {
					return "", fmt.Errorf("scheduledAt must be in the future")
				}
				if !task.ExpiresAt.IsZero() && at.After(task.ExpiresAt) {
					return "", fmt.Errorf("scheduledAt must not be after task expiry")
				}
				task.CronExpression, task.ScheduledAt, task.NextFireAt = "", at, at
			}
			if in.TaskText != "" {
				task.TaskText = in.TaskText
			}
			task.Status = store.ScheduledTaskStatusActive
			if err := t.runner.Update(ctx, *task); err != nil {
				return "", err
			}
			return "rescheduled scheduled task " + id, nil
		}))
}

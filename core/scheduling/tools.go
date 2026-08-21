package scheduling

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"

	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/tools"
)

// Tools are the schedule tools: create, list, cancel. They are registered
// with the provider and excluded from ScheduledTaskScenario runs (see
// agent.ScheduledTaskScenario) — a firing must not schedule more work.
type Tools struct {
	Service *TaskService
	Repos   dao.Backend
}

// NewTools wires the schedule tools.
func NewTools(service *TaskService, repos dao.Backend) *Tools {
	return &Tools{Service: service, Repos: repos}
}

// List returns the tools, for provider registration.
func (t *Tools) List() []tool.InvokableTool {
	return []tool.InvokableTool{t.Create(), t.ListTasks(), t.Cancel()}
}

func (t *Tools) Create() tool.InvokableTool {
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

			if (in.Cron == "") == (in.ScheduledAt == "") {
				return "", fmt.Errorf("set exactly one of cron and scheduledAt")
			}
			task := dao.ScheduledTask{
				ID:            newTaskID(),
				UserID:        userID,
				ChatID:        chatID,
				ChatType:      chatType,
				RootMessageID: rootMessageID,
				TaskText:      in.TaskText,
				Status:        dao.ScheduledTaskStatusActive,
			}
			if in.Cron != "" {
				normalized, err := normalizeCron(in.Cron)
				if err != nil {
					return "", err
				}
				if _, err := ParseCron(normalized); err != nil {
					return "", err
				}
				task.CronExpression = normalized
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
					task.ExpiresAt = time.Now().Add(t.Service.Config.DefaultExpiry)
				}
			default:
				d, err := time.ParseDuration(in.Expiry)
				if err != nil || d <= 0 {
					return "", fmt.Errorf("expiry %q is not a positive duration", in.Expiry)
				}
				task.ExpiresAt = time.Now().Add(d)
			}
			if err := t.Service.Schedule(task); err != nil {
				return "", err
			}
			return "scheduled task " + task.ID, nil
		}))
}

func (t *Tools) ListTasks() tool.InvokableTool {
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
			active, err := t.Repos.ScheduledTasks().FindByUserIDAndStatus(ctx, userID, dao.ScheduledTaskStatusActive)
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

func (t *Tools) Cancel() tool.InvokableTool {
	return tools.MustTool(utils.InferTool(tools.ToolNameCancelScheduledTask,
		"Cancel one of the user's scheduled tasks by id. A firing in progress is stopped too.",
		func(ctx context.Context, in struct {
			TaskID string `json:"taskId"`
		}) (string, error) {
			userID, err := tools.UserID.Require(ctx)
			if err != nil {
				return "", err
			}
			task, err := t.Repos.ScheduledTasks().FindByID(ctx, in.TaskID)
			if err != nil || task == nil || task.UserID != userID {
				return "", fmt.Errorf("no scheduled task %s owned by you", in.TaskID)
			}
			t.Service.Unschedule(in.TaskID)
			if err := t.Repos.ScheduledTasks().UpdateStatus(ctx, in.TaskID, dao.ScheduledTaskStatusCancelled); err != nil {
				return "", err
			}
			return "cancelled task " + in.TaskID, nil
		}))
}

// everySecondOrMinute matches the sub-minute cron forms the normalizer
// rewrites: a */n in the minute field, or any seconds-looking prefix.
var stepMinute = regexp.MustCompile(`^\*/(\d+)$`)

// normalizeCron enforces the minimum interval spring-agent does: a task may
// not fire more often than every five minutes. A model writing */1 or */2
// in the minute field gets */5 rather than a refusal — the intent (recurring
// check) survives; the load does not.
func normalizeCron(expr string) (string, error) {
	fields := strings.Fields(expr)
	if len(fields) != 5 {
		// ParseCron reports the full error; nothing to normalize here.
		return expr, nil
	}
	if m := stepMinute.FindStringSubmatch(fields[0]); m != nil {
		if n, err := strconv.Atoi(m[1]); err == nil && n < 5 {
			fields[0] = "*/5"
		}
	}
	return strings.Join(fields, " "), nil
}

// newTaskID mints an unguessable task id.
func newTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("golem: cannot read random bytes for a task id: " + err.Error())
	}
	return hex.EncodeToString(b)
}

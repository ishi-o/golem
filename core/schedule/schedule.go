package schedule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/prompt"
	"github.com/ishi-o/golem/core/store"
)

// Config configures the runner.
type Config struct {
	// Prompt is the template over {taskText} a firing renders; required.
	Prompt string
	// Scheduler arms and removes the runs; the deployment injects one.
	// Without it the schedule tools refuse new tasks and stored tasks
	// log an error rather than firing.
	Scheduler Scheduler
	// DefaultExpiry bounds a task's life when it names none; 0 means 7
	// days, "never" being an explicit choice the tool's caller makes.
	DefaultExpiry time.Duration
	// Clock supplies wall time for expiry and catch-up decisions.
	Clock func() time.Time
}

// Runner loads the ACTIVE tasks at startup, arms them on the scheduler, and
// keeps the repo's status column telling the truth about what happened. The
// armed jobs are derived state: they are rebuilt from the repo, so a
// restart loses nothing but the wall-clock position of a recurring task.
type Runner struct {
	tasks store.ScheduledTaskStore
	agent *agent.Agent
	cfg   Config
	log   *slog.Logger

	mu      sync.Mutex
	armed   map[string]bool
	running map[string]bool
	stop    bool
}

// New constructs the runner and validates the prompt template. Call Start
// after its dependencies are ready.
func New(tasks store.ScheduledTaskStore, a *agent.Agent, cfg Config, log *slog.Logger) (*Runner, error) {
	if cfg.DefaultExpiry <= 0 {
		cfg.DefaultExpiry = 7 * 24 * time.Hour
	}
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if _, err := prompt.Render(cfg.Prompt, map[string]string{"taskText": ""}); err != nil {
		return nil, fmt.Errorf("golem/schedule: prompt template: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	if tasks == nil {
		return nil, fmt.Errorf("golem/schedule: nil task store")
	}
	return &Runner{tasks: tasks, agent: a, cfg: cfg, log: log, armed: map[string]bool{}, running: map[string]bool{}}, nil
}

// Start loads ACTIVE tasks and arms them. A task whose expiry already passed
// while the process was down is CANCELLED rather than fired: firing it would
// run stale work the user believed gone.
func (r *Runner) Start(ctx context.Context) error {
	active, err := r.tasks.ListByStatus(ctx, store.ScheduledTaskStatusActive)
	if err != nil {
		return err
	}
	now := r.cfg.Clock()
	var failures []error
	for _, task := range active {
		if !task.ExpiresAt.IsZero() && !task.ExpiresAt.After(now) {
			if err := r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusCancelled); err != nil {
				r.log.Warn("expiring stale task failed", "task", task.ID, "err", err)
				failures = append(failures, fmt.Errorf("expire task %s: %w", task.ID, err))
			}
			continue
		}
		fireAt := task.NextFireAt
		if fireAt.IsZero() {
			fireAt = task.ScheduledAt
		}
		if task.CronExpression == "" && !fireAt.IsZero() && !fireAt.After(now) {
			// A one-shot that became due while the process was down is
			// catch-up work. It is intentionally not armed as well, which
			// prevents a scheduler from delivering it twice.
			go r.fire(task)
			continue
		}
		if err := r.arm(task); err != nil {
			failures = append(failures, fmt.Errorf("arm task %s: %w", task.ID, err))
		}
	}
	return errors.Join(failures...)
}

// Stop disarms every task; in-flight firings are the agent's Shutdown's to
// wait for.
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stop = true
	for id := range r.armed {
		if r.cfg.Scheduler != nil {
			r.cfg.Scheduler.Unschedule(id)
		}
		delete(r.armed, id)
	}
}

// Schedule arms and stores a task. The id must be set by the caller (the
// tool mints one); an id-less task cannot be cancelled, so it is refused
// here rather than scheduled unremovably. Arming comes first: an expression
// the scheduler rejects errors without leaving a task row behind, and a
// store failure after a successful arm disarms again rather than leaving a
// job that fires but cannot be found.
func (r *Runner) Schedule(task store.ScheduledTask) error {
	if task.ID == "" {
		return fmt.Errorf("task has no id")
	}
	if task.CronExpression == "" && task.NextFireAt.IsZero() {
		task.NextFireAt = task.ScheduledAt
	}
	if err := r.arm(task); err != nil {
		return err
	}
	if err := r.tasks.Save(context.Background(), task); err != nil {
		r.Unschedule(task.ID)
		return err
	}
	return nil
}

// Update replaces a task's scheduling definition while retaining its id and
// run count. The new definition is armed before it is persisted; a rejected
// schedule therefore does not leave a row claiming it is active.
func (r *Runner) Update(ctx context.Context, task store.ScheduledTask) error {
	if task.ID == "" {
		return fmt.Errorf("task has no id")
	}
	old, err := r.tasks.Get(ctx, task.ID)
	if err != nil {
		return err
	}
	if old == nil {
		return fmt.Errorf("scheduled task %q was not found", task.ID)
	}
	if task.UserID == "" {
		task.UserID = old.UserID
	}
	if task.RunCount == 0 {
		task.RunCount = old.RunCount
	}
	if task.ConversationID == "" {
		task.ConversationID = old.ConversationID
	}
	if task.ChatID == "" {
		task.ChatID = old.ChatID
	}
	if task.ChatType == "" {
		task.ChatType = old.ChatType
	}
	if task.RootMessageID == "" {
		task.RootMessageID = old.RootMessageID
	}
	if task.GroupID == "" {
		task.GroupID = old.GroupID
	}
	if task.TenantID == "" {
		task.TenantID = old.TenantID
	}
	r.disarm(task.ID)
	if task.Status == "" {
		task.Status = store.ScheduledTaskStatusActive
	}
	if task.Status == store.ScheduledTaskStatusActive {
		if err := r.arm(task); err != nil {
			_ = r.arm(*old)
			return err
		}
	}
	if err := r.tasks.Save(ctx, task); err != nil {
		r.disarm(task.ID)
		if old.Status == store.ScheduledTaskStatusActive {
			_ = r.arm(*old)
		}
		return err
	}
	return nil
}

// Unschedule removes a task's job and stops its firing run. requestId
// equals the task id, so a firing in flight is abortable by the same call.
func (r *Runner) Unschedule(taskID string) {
	r.disarm(taskID)
	if r.agent != nil {
		r.agent.Cancel(taskID)
	}
}

// StopFiringTask completes the task whose current firing called for it. It
// deliberately does not cancel the agent run: the run must be able to finish
// the answer that explains why the recurring task stopped.
func (r *Runner) StopFiringTask(ctx context.Context, taskID string) error {
	if err := r.tasks.SetStatus(ctx, taskID, store.ScheduledTaskStatusCompleted); err != nil {
		return err
	}
	r.disarm(taskID)
	return nil
}

func (r *Runner) disarm(taskID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.armed[taskID] {
		if r.cfg.Scheduler != nil {
			r.cfg.Scheduler.Unschedule(taskID)
		}
		delete(r.armed, taskID)
	}
}

// arm hands one task to the scheduler. The cron expression was normalized
// and validated at creation; one that the scheduler now rejects is a corrupt
// row, reported loudly rather than silently never firing.
func (r *Runner) arm(task store.ScheduledTask) error {
	if err := validateSchedule(task); err != nil {
		return err
	}
	if r.cfg.Scheduler == nil {
		r.log.Error("no scheduler configured; task will not fire", "task", task.ID)
		return fmt.Errorf("no scheduler configured")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.stop {
		return fmt.Errorf("runner is stopped")
	}
	run := func() { r.fire(task) }
	if task.CronExpression != "" {
		if err := r.cfg.Scheduler.ScheduleCron(task.ID, task.CronExpression, run); err != nil {
			r.log.Error("arming cron task failed", "task", task.ID, "cron", task.CronExpression, "err", err)
			return err
		}
	} else {
		at := task.NextFireAt
		if at.IsZero() {
			at = task.ScheduledAt
		}
		r.cfg.Scheduler.ScheduleAt(task.ID, at, run)
	}
	r.armed[task.ID] = true
	return nil
}

// fire runs one firing of a task. A firing shares the creating thread's
// conversation so it reads earlier firings and the user's messages.
func (r *Runner) fire(task store.ScheduledTask) {
	ctx := context.Background()
	r.mu.Lock()
	if r.running[task.ID] {
		r.mu.Unlock()
		return
	}
	r.running[task.ID] = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.running, task.ID)
		r.mu.Unlock()
	}()
	stored, err := r.tasks.Get(ctx, task.ID)
	if err != nil {
		r.log.Error("loading scheduled task before firing failed", "task", task.ID, "err", err)
		return
	}
	if stored == nil {
		return
	}
	task = *stored
	if task.Status != store.ScheduledTaskStatusActive {
		return
	}
	if r.agent == nil || !r.agent.Accepting() {
		if r.agent == nil {
			_ = r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusFailed)
		}
		return
	}
	if !task.ExpiresAt.IsZero() && !task.ExpiresAt.After(r.cfg.Clock()) {
		r.Unschedule(task.ID)
		_ = r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusCancelled)
		return
	}
	if task.MaxRuns > 0 && task.RunCount >= task.MaxRuns {
		r.Unschedule(task.ID)
		_ = r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusCompleted)
		return
	}
	task.RunCount++
	if err := r.tasks.Save(ctx, task); err != nil {
		r.log.Error("recording scheduled task run failed", "task", task.ID, "err", err)
		return
	}
	text, err := prompt.Render(r.cfg.Prompt, map[string]string{"taskText": task.TaskText})
	if err != nil {
		// The template was validated at startup; a render failure means the
		// task text itself carries a brace. The raw text goes to the model
		// rather than the firing being dropped.
		r.log.Warn("scheduled task prompt render failed; using raw task text", "task", task.ID, "err", err)
		text = task.TaskText
	}
	conversationID := task.ConversationID
	if conversationID == "" {
		conversationID = task.RootMessageID
	}
	if conversationID == "" {
		conversationID = task.ChatID
	}
	rootMessageID := task.RootMessageID
	if rootMessageID == "" {
		rootMessageID = task.ChatID
	}
	err = r.agent.Fire(agent.NewRequest(agent.ScheduledTaskScenario, text,
		agent.WithRequestID(task.ID),
		agent.WithIdentity(task.UserID, task.ChatID, task.ChatType),
		agent.WithScope(task.GroupID, task.TenantID),
		agent.WithConversation(conversationID, rootMessageID, rootMessageID),
		agent.WithBackground(task.Background),
		agent.WithScheduledTaskID(task.ID),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(outcome agent.Outcome) {
			current, getErr := r.tasks.Get(ctx, task.ID)
			if getErr != nil || current == nil || current.Status != store.ScheduledTaskStatusActive {
				return
			}
			// A self-reschedule or an external update owns the next lifecycle;
			// this firing must not complete the replacement definition.
			if current.RunCount != task.RunCount || current.CronExpression != task.CronExpression || !current.ScheduledAt.Equal(task.ScheduledAt) || current.MaxRuns != task.MaxRuns {
				return
			}
			// A cron task stays ACTIVE whatever one firing did; a one-shot
			// is done after one.
			if task.CronExpression != "" && (task.MaxRuns <= 0 || task.RunCount < task.MaxRuns) {
				return
			}
			if task.CronExpression != "" && task.MaxRuns > 0 && task.RunCount >= task.MaxRuns {
				r.Unschedule(task.ID)
			}
			switch outcome {
			case agent.OutcomeCompleted:
				_ = r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusCompleted)
			case agent.OutcomeFailed:
				_ = r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusFailed)
			case agent.OutcomeCancelled:
				_ = r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusCancelled)
			}
		}}),
	))
	if err != nil {
		r.log.Error("firing scheduled task failed", "task", task.ID, "err", err)
	}
}

func validateSchedule(task store.ScheduledTask) error {
	if task.ID == "" {
		return fmt.Errorf("task has no id")
	}
	if task.CronExpression != "" && !task.ScheduledAt.IsZero() {
		return fmt.Errorf("cron and scheduledAt are mutually exclusive")
	}
	if task.CronExpression == "" && task.ScheduledAt.IsZero() && task.NextFireAt.IsZero() {
		return fmt.Errorf("scheduledAt is required for a one-shot task")
	}
	if task.MaxRuns < 0 || task.RunCount < 0 {
		return fmt.Errorf("task run limits cannot be negative")
	}
	return nil
}

// newTaskID mints an unguessable task id.
func newTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("golem: cannot read random bytes for a task id: " + err.Error())
	}
	return hex.EncodeToString(b)
}

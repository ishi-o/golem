package schedule

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

	mu    sync.Mutex
	armed map[string]bool
	stop  bool
}

// New constructs the runner and validates the prompt template. Call Start
// after its dependencies are ready.
func New(tasks store.ScheduledTaskStore, a *agent.Agent, cfg Config, log *slog.Logger) (*Runner, error) {
	if cfg.DefaultExpiry <= 0 {
		cfg.DefaultExpiry = 7 * 24 * time.Hour
	}
	if _, err := prompt.Render(cfg.Prompt, map[string]string{"taskText": ""}); err != nil {
		return nil, fmt.Errorf("golem/schedule: prompt template: %w", err)
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{tasks: tasks, agent: a, cfg: cfg, log: log, armed: map[string]bool{}}, nil
}

// Start loads ACTIVE tasks and arms them. A task whose expiry already passed
// while the process was down is CANCELLED rather than fired: firing it would
// run stale work the user believed gone.
func (r *Runner) Start(ctx context.Context) error {
	active, err := r.tasks.ListByStatus(ctx, store.ScheduledTaskStatusActive)
	if err != nil {
		return err
	}
	for _, task := range active {
		if !task.ExpiresAt.IsZero() && !task.ExpiresAt.After(time.Now()) {
			if err := r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusCancelled); err != nil {
				r.log.Warn("expiring stale task failed", "task", task.ID, "err", err)
			}
			continue
		}
		r.arm(task)
	}
	return nil
}

// Stop disarms every task; in-flight firings are the agent's Shutdown's to
// wait for.
func (r *Runner) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stop = true
	for id := range r.armed {
		r.cfg.Scheduler.Unschedule(id)
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
	if err := r.arm(task); err != nil {
		return err
	}
	if err := r.tasks.Save(context.Background(), task); err != nil {
		r.Unschedule(task.ID)
		return err
	}
	return nil
}

// Unschedule removes a task's job and stops its firing run. requestId
// equals the task id, so a firing in flight is abortable by the same call.
func (r *Runner) Unschedule(taskID string) {
	r.mu.Lock()
	if r.armed[taskID] {
		r.cfg.Scheduler.Unschedule(taskID)
		delete(r.armed, taskID)
	}
	r.mu.Unlock()
	r.agent.Cancel(taskID)
}

// arm hands one task to the scheduler. The cron expression was normalized
// and validated at creation; one that the scheduler now rejects is a corrupt
// row, reported loudly rather than silently never firing.
func (r *Runner) arm(task store.ScheduledTask) error {
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
		r.cfg.Scheduler.ScheduleAt(task.ID, task.ScheduledAt, run)
	}
	r.armed[task.ID] = true
	return nil
}

// fire runs one firing of a task. A firing shares the creating thread's
// conversation so it reads earlier firings and the user's messages.
func (r *Runner) fire(task store.ScheduledTask) {
	ctx := context.Background()
	if !r.agent.Accepting() {
		return
	}
	if !task.ExpiresAt.IsZero() && !task.ExpiresAt.After(time.Now()) {
		r.Unschedule(task.ID)
		_ = r.tasks.SetStatus(ctx, task.ID, store.ScheduledTaskStatusCancelled)
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
	err = r.agent.Fire(agent.NewRequest(agent.ScheduledTaskScenario, text,
		agent.WithRequestID(task.ID),
		agent.WithIdentity(task.UserID, task.ChatID, task.ChatType),
		agent.WithConversation(task.RootMessageID, task.RootMessageID, task.RootMessageID),
		agent.WithBackground(task.Background),
		agent.WithListener(agent.ListenerFuncs{OnFinishedFunc: func(outcome agent.Outcome) {
			// A cron task stays ACTIVE whatever one firing did; a one-shot
			// is done after one.
			if task.CronExpression != "" {
				return
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

// newTaskID mints an unguessable task id.
func newTaskID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("golem: cannot read random bytes for a task id: " + err.Error())
	}
	return hex.EncodeToString(b)
}

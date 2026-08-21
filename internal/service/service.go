package service

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/dao"
	"github.com/ishi-o/golem/core/prompt"
)

// TaskService loads the ACTIVE tasks at startup, fires them at their time,
// and keeps the repo's status column telling the truth about what happened.
// The in-memory schedule is derived state: it is rebuilt from the repo, so a
// restart loses nothing but the wall-clock position of a recurring task.
type TaskService struct {
	Tasks  dao.ScheduledTaskRepo
	Agent  *agent.Agent
	Config ScheduledTaskConfig
	Log    *slog.Logger

	nowFunc func() time.Time

	mu      sync.Mutex
	timers  map[string]*time.Timer
	cancels map[string]context.CancelFunc
	stop    bool
}

// ScheduledTaskConfig configures firing.
type ScheduledTaskConfig struct {
	// ScheduledTaskPrompt is the template over {taskText} a firing renders.
	ScheduledTaskPrompt string
	// DefaultExpiry bounds a task's life when it names none; 0 means 7
	// days, "never" being an explicit choice the tool's caller makes.
	DefaultExpiry time.Duration
}

// New wires the service. Call Start once wiring is done.
func New(tasks dao.ScheduledTaskRepo, a *agent.Agent, cfg ScheduledTaskConfig, log *slog.Logger) *TaskService {
	if cfg.DefaultExpiry <= 0 {
		cfg.DefaultExpiry = 7 * 24 * time.Hour
	}
	if log == nil {
		log = slog.Default()
	}
	return &TaskService{Tasks: tasks, Agent: a, Config: cfg, Log: log, timers: map[string]*time.Timer{}, cancels: map[string]context.CancelFunc{}}
}

// Start loads ACTIVE tasks and schedules them. A task whose expiry already
// passed while the process was down is CANCELLED rather than fired: firing
// it would run stale work the user believed gone.
func (s *TaskService) Start(ctx context.Context) error {
	active, err := s.Tasks.FindByStatus(ctx, dao.ScheduledTaskStatusActive)
	if err != nil {
		return err
	}
	for _, task := range active {
		if !task.ExpiresAt.IsZero() && !task.ExpiresAt.After(s.now()) {
			if err := s.Tasks.UpdateStatus(ctx, task.ID, dao.ScheduledTaskStatusCancelled); err != nil {
				s.Log.Warn("expiring stale task failed", "task", task.ID, "err", err)
			}
			continue
		}
		s.schedule(task)
	}
	return nil
}

// Stop cancels every timer; in-flight firings are the agent's Shutdown's to
// wait for.
func (s *TaskService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stop = true
	for id, t := range s.timers {
		t.Stop()
		delete(s.timers, id)
	}
	for id, c := range s.cancels {
		c()
		delete(s.cancels, id)
	}
}

// Schedule stores and schedules a task. The id must be set by the caller
// (the tool mints one); an id-less task cannot be cancelled, so it is
// refused here rather than scheduled unremovably.
func (s *TaskService) Schedule(task dao.ScheduledTask) error {
	if task.ID == "" {
		return fmt.Errorf("task has no id")
	}
	ctx := context.Background()
	if err := s.Tasks.Save(ctx, task); err != nil {
		return err
	}
	s.schedule(task)
	return nil
}

// Unschedule removes a task's timer and stops its firing run. requestId
// equals the task id, so a firing in flight is abortable by the same call.
func (s *TaskService) Unschedule(taskID string) {
	s.mu.Lock()
	if t, ok := s.timers[taskID]; ok {
		t.Stop()
		delete(s.timers, taskID)
	}
	cancel, ok := s.cancels[taskID]
	if ok {
		cancel()
		delete(s.cancels, taskID)
	}
	s.mu.Unlock()
	s.Agent.Cancel(taskID)
}

// schedule arms the timer for one task: a cron task re-arms after each
// firing; a one-shot arms for its instant, firing immediately when that has
// already passed (the log line is the warning; the task was due).
func (s *TaskService) schedule(task dao.ScheduledTask) {
	next, isCron := time.Time{}, false
	if task.CronExpression != "" {
		cron, err := ParseCron(task.CronExpression)
		if err != nil {
			// A task that cannot be parsed was validated at creation; a
			// corrupt row is failed loudly rather than silently never
			// firing.
			s.Log.Error("stored cron unparsable; task will not fire", "task", task.ID, "cron", task.CronExpression, "err", err)
			return
		}
		next, isCron = cron.Next(s.now()), true
	} else {
		next = task.ScheduledAt
	}
	delay := time.Until(next)
	if delay < 0 {
		delay = 0
		s.Log.Warn("scheduled task is past due; firing now", "task", task.ID, "due", next.Format(time.RFC3339))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stop {
		return
	}
	s.timers[task.ID] = time.AfterFunc(delay, func() { s.fire(task, isCron) })
}

// fire runs one firing of a task. A firing shares the creating thread's
// conversation so it reads earlier firings and the user's messages.
func (s *TaskService) fire(task dao.ScheduledTask, isCron bool) {
	ctx := context.Background()
	if !s.Agent.Accepting() {
		return
	}
	if !task.ExpiresAt.IsZero() && !task.ExpiresAt.After(s.now()) {
		s.Unschedule(task.ID)
		_ = s.Tasks.UpdateStatus(ctx, task.ID, dao.ScheduledTaskStatusCancelled)
		return
	}
	if isCron {
		s.schedule(task)
	}
	text, err := prompt.Render(s.Config.ScheduledTaskPrompt, map[string]string{"taskText": task.TaskText})
	if err != nil {
		// The template was validated at startup; a render failure means the
		// task text itself carries a brace. The raw text goes to the model
		// rather than the firing being dropped.
		s.Log.Warn("scheduled task prompt render failed; using raw task text", "task", task.ID, "err", err)
		text = task.TaskText
	}
	err = s.Agent.Fire(agent.NewRequest(agent.ScheduledTaskScenario, text,
		agent.WithRequestID(task.ID),
		agent.WithIdentity(task.UserID, task.ChatID, task.ChatType),
		agent.WithConversation(task.RootMessageID, task.RootMessageID, task.RootMessageID),
		agent.WithBackground(task.Background),
		agent.WithListener(agent.ListenerFuncs{OnFinishedF: func(outcome agent.Outcome) {
			// A cron task stays ACTIVE whatever one firing did; a one-shot
			// is done after one.
			if isCron {
				return
			}
			switch outcome {
			case agent.OutcomeCompleted:
				_ = s.Tasks.UpdateStatus(ctx, task.ID, dao.ScheduledTaskStatusCompleted)
			case agent.OutcomeFailed:
				_ = s.Tasks.UpdateStatus(ctx, task.ID, dao.ScheduledTaskStatusFailed)
			case agent.OutcomeCancelled:
				_ = s.Tasks.UpdateStatus(ctx, task.ID, dao.ScheduledTaskStatusCancelled)
			}
		}}),
	))
	if err != nil {
		s.Log.Error("firing scheduled task failed", "task", task.ID, "err", err)
	}
}

func (s *TaskService) now() time.Time {
	if s.nowFunc != nil {
		return s.nowFunc()
	}
	return time.Now()
}

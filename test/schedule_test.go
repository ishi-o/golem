package agent_test

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ishi-o/golem/core/agent"
	"github.com/ishi-o/golem/core/config"
	"github.com/ishi-o/golem/core/schedule"
	"github.com/ishi-o/golem/core/storage"
	"github.com/ishi-o/golem/core/store"
	"github.com/ishi-o/golem/core/tools"
)

// fakeScheduler arms runs without a clock: the test triggers them by task
// id. failExpr, when set, is the expression ScheduleCron refuses.
type fakeScheduler struct {
	mu          sync.Mutex
	cron        map[string]cronJob
	at          map[string]func()
	unscheduled []string
	failExpr    string
}

type cronJob struct {
	expr string
	run  func()
}

func newFakeScheduler() *fakeScheduler {
	return &fakeScheduler{cron: map[string]cronJob{}, at: map[string]func(){}}
}

func (s *fakeScheduler) ScheduleCron(id, expr string, run func()) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if expr == s.failExpr {
		return fmt.Errorf("unparsable cron %q", expr)
	}
	s.cron[id] = cronJob{expr: expr, run: run}
	return nil
}

func (s *fakeScheduler) ScheduleAt(id string, at time.Time, run func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.at[id] = run
}

func (s *fakeScheduler) Unschedule(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cron, id)
	delete(s.at, id)
	s.unscheduled = append(s.unscheduled, id)
}

// trigger fires one armed run, as the clock would have.
func (s *fakeScheduler) trigger(t *testing.T, id string) {
	t.Helper()
	s.mu.Lock()
	var run func()
	if job, ok := s.cron[id]; ok {
		run = job.run
	} else if r, ok := s.at[id]; ok {
		run = r
	}
	s.mu.Unlock()
	require.NotNil(t, run, "no armed run for task %s", id)
	run()
}

func (s *fakeScheduler) armedCron(id string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.cron[id]
	return job.expr, ok
}

func (s *fakeScheduler) armedAt(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.at[id]
	return ok
}

func (s *fakeScheduler) wasUnscheduled(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.unscheduled {
		if u == id {
			return true
		}
	}
	return false
}

// newRunner builds a runner over the fixture's store and one agent whose
// model scripts one completed turn per firing.
func newRunner(t *testing.T, firings int, s schedule.Scheduler) (*schedule.Runner, store.Backend) {
	t.Helper()
	turns := make([][]*schema.Message, firings)
	for i := range turns {
		turns[i] = textChunks("the firing ran")
	}
	a, fixture := newTestAgent(t, &fakeModel{turns: turns})
	backend := fixture.Backend()
	runner, err := schedule.New(backend.ScheduledTasks(), a, schedule.Config{
		Prompt:    "A task fired: {taskText}",
		Scheduler: s,
	}, nil)
	require.NoError(t, err)
	return runner, backend
}

func waitForStatus(t *testing.T, tasks store.ScheduledTaskStore, id string, status store.ScheduledTaskStatus) {
	t.Helper()
	require.Eventually(t, func() bool {
		task, err := tasks.Get(context.Background(), id)
		return err == nil && task != nil && task.Status == status
	}, 5*time.Second, 10*time.Millisecond, "task %s never became %s", id, status)
}

func TestScheduleRunnerCancelsStaleExpiredTasksAtStart(t *testing.T) {
	runner, backend := newRunner(t, 0, newFakeScheduler())
	tasks := backend.ScheduledTasks()
	stale := store.ScheduledTask{ID: "stale", UserID: "u", Status: store.ScheduledTaskStatusActive,
		ScheduledAt: time.Now().Add(time.Hour), ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, tasks.Save(context.Background(), stale))

	require.NoError(t, runner.Start(context.Background()))
	task, err := tasks.Get(context.Background(), "stale")
	require.NoError(t, err)
	assert.Equal(t, store.ScheduledTaskStatusCancelled, task.Status)
}

func TestScheduleRunnerOneShotCompletesAfterFiring(t *testing.T) {
	s := newFakeScheduler()
	runner, backend := newRunner(t, 1, s)
	tasks := backend.ScheduledTasks()
	task := store.ScheduledTask{ID: "once", UserID: "u", Status: store.ScheduledTaskStatusActive,
		TaskText: "say hi", ScheduledAt: time.Now().Add(time.Hour)}
	require.NoError(t, tasks.Save(context.Background(), task))

	require.NoError(t, runner.Start(context.Background()))
	require.True(t, s.armedAt("once"))
	s.trigger(t, "once")
	waitForStatus(t, tasks, "once", store.ScheduledTaskStatusCompleted)
}

func TestScheduleRunnerCronStaysActiveAcrossFirings(t *testing.T) {
	s := newFakeScheduler()
	runner, backend := newRunner(t, 2, s)
	tasks := backend.ScheduledTasks()
	task := store.ScheduledTask{ID: "cron", UserID: "u", Status: store.ScheduledTaskStatusActive,
		TaskText: "heartbeat", CronExpression: "*/5 * * * *"}
	require.NoError(t, tasks.Save(context.Background(), task))

	require.NoError(t, runner.Start(context.Background()))
	expr, ok := s.armedCron("cron")
	require.True(t, ok)
	assert.Equal(t, "*/5 * * * *", expr)

	s.trigger(t, "cron")
	waitForStatus(t, tasks, "cron", store.ScheduledTaskStatusActive)
	s.trigger(t, "cron")
	waitForStatus(t, tasks, "cron", store.ScheduledTaskStatusActive)
	_, armed := s.armedCron("cron")
	assert.True(t, armed, "a cron task stays armed after a firing")
}

func TestScheduleRunnerExpiryCancelsAtFireTime(t *testing.T) {
	s := newFakeScheduler()
	runner, backend := newRunner(t, 0, s)
	tasks := backend.ScheduledTasks()
	// Schedule (not Start) so the task is armed despite being expired;
	// Start would have cancelled it before arming.
	task := store.ScheduledTask{ID: "late", UserID: "u", Status: store.ScheduledTaskStatusActive,
		TaskText: "too late", ScheduledAt: time.Now().Add(time.Hour), ExpiresAt: time.Now().Add(-time.Minute)}
	require.NoError(t, runner.Schedule(task))

	s.trigger(t, "late")
	waitForStatus(t, tasks, "late", store.ScheduledTaskStatusCancelled)
	assert.True(t, s.wasUnscheduled("late"))
}

func TestScheduleRunnerArmFailurePersistsNothing(t *testing.T) {
	s := newFakeScheduler()
	s.failExpr = "bad"
	runner, backend := newRunner(t, 0, s)
	tasks := backend.ScheduledTasks()

	err := runner.Schedule(store.ScheduledTask{ID: "bad", UserID: "u", Status: store.ScheduledTaskStatusActive,
		TaskText: "x", CronExpression: "bad"})
	require.Error(t, err)
	// Arming validates before persisting: an expression the scheduler
	// rejects never becomes a task row.
	task, gerr := tasks.Get(context.Background(), "bad")
	require.NoError(t, gerr)
	assert.Nil(t, task)
}

func TestScheduleNewRejectsUnknownPromptVariable(t *testing.T) {
	_, backend := newRunner(t, 0, newFakeScheduler())
	_, err := schedule.New(backend.ScheduledTasks(), nil, schedule.Config{
		Prompt:    "refers to {unknown}",
		Scheduler: newFakeScheduler(),
	}, nil)
	require.Error(t, err)
}

func TestScheduleToolsValidateAndScope(t *testing.T) {
	s := newFakeScheduler()
	runner, backend := newRunner(t, 0, s)
	tasks := backend.ScheduledTasks()
	family := schedule.NewTools(runner, tasks)
	ctx := tools.UserID.With(tools.ChatID.With(context.Background(), "chat-1"), "u1")
	create := findTool(t, family.List(), tools.ToolNameCreateScheduledTask)
	list := findTool(t, family.List(), tools.ToolNameListScheduledTasks)
	cancel := findTool(t, family.List(), tools.ToolNameCancelScheduledTask)

	// Exactly one of cron and scheduledAt.
	for _, args := range []string{
		`{"taskText":"x"}`,
		`{"taskText":"x","cron":"*/5 * * * *","scheduledAt":"2026-01-01T00:00:00Z"}`,
	} {
		_, err := create.InvokableRun(ctx, args)
		require.Error(t, err, "args %s should have been refused", args)
	}

	// Bad time and bad expiry are refused.
	_, err := create.InvokableRun(ctx, `{"taskText":"x","scheduledAt":"tomorrow"}`)
	require.Error(t, err)
	_, err = create.InvokableRun(ctx, `{"taskText":"x","scheduledAt":"2026-01-01T00:00:00Z","expiry":"abc"}`)
	require.Error(t, err)

	// A cron task is stored verbatim (the scheduler is the sole validator)
	// and armed. An expression the scheduler rejects leaves no row.
	out := invokeTool(t, create, ctx, `{"taskText":"x","cron":"*/1 * * * *"}`)
	id := strings.TrimSpace(strings.TrimPrefix(out, "scheduled task "))
	task, err := tasks.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, "*/1 * * * *", task.CronExpression)
	assert.Equal(t, store.ScheduledTaskStatusActive, task.Status)
	expr, armed := s.armedCron(id)
	require.True(t, armed)
	assert.Equal(t, "*/1 * * * *", expr)

	s.failExpr = "bad"
	_, err = create.InvokableRun(ctx, `{"taskText":"z","cron":"bad"}`)
	require.Error(t, err)
	rejected, gerr := tasks.ListByStatus(ctx, store.ScheduledTaskStatusActive)
	require.NoError(t, gerr)
	for _, st := range rejected {
		assert.NotEqual(t, "bad", st.CronExpression, "a rejected expression must not be persisted")
	}

	// "never" leaves the task without an expiry.
	out = invokeTool(t, create, ctx, `{"taskText":"y","scheduledAt":"2026-01-01T00:00:00Z","expiry":"never"}`)
	id2 := strings.TrimSpace(strings.TrimPrefix(out, "scheduled task "))
	task2, err := tasks.Get(ctx, id2)
	require.NoError(t, err)
	assert.True(t, task2.ExpiresAt.IsZero())

	// The list is scoped to the asking user.
	other := tools.UserID.With(context.Background(), "u2")
	invokeTool(t, create, other, `{"taskText":"theirs","scheduledAt":"2026-01-01T00:00:00Z"}`)
	assert.Contains(t, invokeTool(t, list, ctx, `{}`), `"text":"x"`)
	assert.NotContains(t, invokeTool(t, list, ctx, `{}`), "theirs")

	// Cancel is refused for a task the caller does not own.
	_, err = cancel.InvokableRun(other, fmt.Sprintf(`{"taskId":%q}`, id))
	require.Error(t, err)
	invokeTool(t, cancel, ctx, fmt.Sprintf(`{"taskId":%q}`, id))
	task, err = tasks.Get(ctx, id)
	require.NoError(t, err)
	assert.Equal(t, store.ScheduledTaskStatusCancelled, task.Status)
	assert.True(t, s.wasUnscheduled(id))
}

func TestScheduleToolsRefusedWithoutScheduler(t *testing.T) {
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })
	backend := fixture.Backend()
	runner, err := schedule.New(backend.ScheduledTasks(), nil, schedule.Config{Prompt: "p {taskText}"}, nil)
	require.NoError(t, err)
	family := schedule.NewTools(runner, backend.ScheduledTasks())
	create := findTool(t, family.List(), tools.ToolNameCreateScheduledTask)
	_, err = create.InvokableRun(tools.UserID.With(context.Background(), "u"), `{"taskText":"x","scheduledAt":"2026-01-01T00:00:00Z"}`)
	require.ErrorContains(t, err, "not configured")
}

func TestScheduledScenarioExcludesScheduleTools(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{}
	require.NoError(t, cfg.Normalize())
	cfg.Storage.Location = dir
	fixture := newSQLXFixture(t)
	t.Cleanup(func() { require.NoError(t, fixture.Close()) })
	backend := fixture.Backend()

	provider := tools.NewProvider(cfg, storage.NewWorkspaceFactory(dir), backend, nil)
	runner, err := schedule.New(backend.ScheduledTasks(), nil, schedule.Config{Prompt: "p {taskText}"}, nil)
	require.NoError(t, err)
	schedule.RegisterBuiltins(provider, schedule.NewTools(runner, backend.ScheduledTasks()))

	names := func(offers func(string) bool) map[string]bool {
		t.Helper()
		comp, err := provider.Compose(context.Background(), tools.ComposeRequest{
			ScenarioOffers: offers,
			UserID:         "u",
		})
		require.NoError(t, err)
		t.Cleanup(comp.Close)
		out := map[string]bool{}
		for _, info := range comp.Info {
			out[info.Name] = true
		}
		return out
	}

	firing := names(agent.ScheduledTaskScenario.Offers)
	for _, name := range []string{tools.ToolNameCreateScheduledTask, tools.ToolNameListScheduledTasks, tools.ToolNameCancelScheduledTask} {
		assert.False(t, firing[name], "a firing must not schedule more work: %s offered", name)
	}
	assert.True(t, firing[tools.ToolNameReadFile], "the file tools remain offered to a firing")

	chat := names(agent.ChatScenario.Offers)
	for _, name := range []string{tools.ToolNameCreateScheduledTask, tools.ToolNameListScheduledTasks, tools.ToolNameCancelScheduledTask} {
		assert.True(t, chat[name], "a chat run offers %s", name)
	}
}

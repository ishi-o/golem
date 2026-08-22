# Scheduled tasks

`core/schedule` persists the user's scheduled tasks, reloads the ACTIVE
ones at startup, and fires them at the agent in the creating conversation.
It ships no scheduler on purpose: **you inject one**, wrapping your
scheduler library in a three-method interface. The injected scheduler is
also the sole validator of cron expressions — golem parses nothing.

## The seam

```go
// core/schedule
type Scheduler interface {
    // ScheduleCron arms run on each match of the cron expression; recurring.
    // Re-arming after a firing is the implementation's job. An expression
    // the library cannot parse is an error — this is creation-time validation.
    ScheduleCron(id string, expr string, run func()) error
    // ScheduleAt arms run to fire once at the instant; a past instant fires
    // immediately.
    ScheduleAt(id string, at time.Time, run func())
    // Unschedule removes the job, stopping a run in flight if it can.
    Unschedule(id string)
}
```

With robfig/cron, for example:

```go
type robfigScheduler struct{ c *cron.Cron }

func (s robfigScheduler) ScheduleCron(id, expr string, run func()) error {
    _, err := s.c.AddFunc(expr, run) // re-arming on each match is the library's
    return err                       // job; an unparsable expr is the error
}

func (s robfigScheduler) ScheduleAt(id string, at time.Time, run func()) {
    s.c.Schedule(oneTime{at}, cron.FuncJob(run)) // a small Schedule wrapper
}

func (s robfigScheduler) Unschedule(id string) { /* Remove by entry */ }
```

A deployment wanting a minimum firing interval (five minutes, say) rejects
sub-floor expressions in its adapter — golem enforces no policy of its own.

## Wiring

```go
runner, err := schedule.New(backend.ScheduledTasks(), a, schedule.Config{
    Prompt:    cfg.AI.ScheduledTaskPrompt, // template over {taskText}; validated in New
    Scheduler: robfigScheduler{c: cron.New()},
}, logger)
if err != nil { return err }
if err := runner.Start(ctx); err != nil { return err } // reload ACTIVE tasks
schedule.RegisterBuiltins(provider, schedule.NewTools(runner, backend.ScheduledTasks()))
// shutdown order: runner.Stop() before agent.Shutdown, so no new firing
// starts while old ones drain.
```

Without a scheduler the schedule tools are simply not offered.

## Lifecycle

- **Creation** (`CreateScheduledTask`): exactly one of a 5-field `cron`
  expression or an RFC 3339 `scheduledAt`; an optional `expiry` duration
  (default 7 days, `"never"` is explicit). The runner arms the task
  **before** persisting it — an expression the scheduler rejects errors
  with no task row left behind.
- **Reload**: `Start` loads ACTIVE tasks; one whose expiry passed while the
  process was down is CANCELLED, not fired — firing it would run stale work
  the user believed gone.
- **Firing**: renders the prompt over `{taskText}` (raw text on a render
  failure) and fires `agent.ScheduledTaskScenario` with the task id as
  request id, resuming the creating conversation. Expiry is checked at fire
  time too.
- **Statuses**: a one-shot becomes COMPLETED, FAILED or CANCELLED with its
  run's outcome; a cron task stays ACTIVE whatever one firing did.
- **Cancellation** (`CancelScheduledTask`): owner-checked; disarms the job
  and aborts a firing in flight (the request id equals the task id).

The store is the source of truth: the armed jobs are derived state, rebuilt
from it, so a restart loses nothing but the wall-clock position of a
recurring task.

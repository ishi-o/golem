// Package schedule runs the user's scheduled tasks: it persists them, arms
// them, and fires them at the agent at their time.
//
// The package ships no scheduler on purpose — the deployment injects one by
// wrapping its scheduler library. With robfig/cron, for example:
//
//	type robfigScheduler struct{ c *cron.Cron }
//
//	func (s robfigScheduler) ScheduleCron(id, expr string, run func()) error {
//		_, err := s.c.AddFunc(expr, run) // re-arming on each match is the library's
//		return err                       // job; an unparsable expr is the error
//	}
//
//	func (s robfigScheduler) ScheduleAt(id string, at time.Time, run func()) {
//		s.c.Schedule(oneTime{at}, cron.FuncJob(run)) // a small Schedule wrapper
//	}
//
//	func (s robfigScheduler) Unschedule(id string) { ... }
//
//	runner, err := schedule.New(backend.ScheduledTasks(), a, schedule.Config{
//		Prompt:    cfg.AI.ScheduledTaskPrompt,
//		Scheduler: robfigScheduler{c: c},
//	}, logger)
//	if err != nil {
//		return err
//	}
//	if err := runner.Start(ctx); err != nil {
//		return err
//	}
//	schedule.RegisterBuiltins(provider, schedule.NewTools(runner, backend.ScheduledTasks()))
//
// The store is the source of truth: Start reloads the ACTIVE tasks and arms
// them, so a restart loses nothing but the wall-clock position of a
// recurring task.
package schedule

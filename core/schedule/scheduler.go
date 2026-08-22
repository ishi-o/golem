package schedule

import "time"

// Scheduler arms and removes task runs. golem ships no implementation on
// purpose: the deployment wraps its scheduler library (gocron, robfig/cron,
// ...) in this interface. The implementation is also the sole validator of
// cron expressions — golem parses nothing; a deployment wanting a minimum
// firing interval enforces it here.
type Scheduler interface {
	// ScheduleCron arms run to fire on each match of the cron expression;
	// the task is recurring, and re-arming after a firing is the
	// implementation's job. An expression the library cannot parse is
	// reported as an error — this is how creation-time validation works.
	ScheduleCron(id string, expr string, run func()) error
	// ScheduleAt arms run to fire once at the instant. An instant already
	// passed fires immediately; the task was due.
	ScheduleAt(id string, at time.Time, run func())
	// Unschedule removes the job. An implementation that can stop a run in
	// flight should do so.
	Unschedule(id string)
}

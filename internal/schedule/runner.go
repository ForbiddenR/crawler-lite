package schedule

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/yourteam/crawler-lite/internal/task"
)

// TaskQueuer is the slice of task.Service the runner needs to create
// scheduled tasks. Defined here, in the consumer.
type TaskQueuer interface {
	Queue(ctx context.Context, in task.CreateInput) (*task.Task, error)
}

// Runner owns the in-process cron daemon. There is exactly one Runner per
// master. It rebuilds its registration set whenever Reload is called — that
// happens once at startup and again after every schedule mutation.
//
// The cron lib runs jobs in its own goroutine pool; we just hold the
// instance, register closures, and start/stop it via Run/ctx cancellation.
type Runner struct {
	svc    *Service
	queuer TaskQueuer
	log    *slog.Logger

	mu   sync.Mutex
	cron *cron.Cron
}

func NewRunner(svc *Service, q TaskQueuer, log *slog.Logger) *Runner {
	return &Runner{svc: svc, queuer: q, log: log}
}

// Run starts the cron and blocks until ctx is cancelled, at which point the
// cron is stopped (Stop waits for in-flight jobs).
func (r *Runner) Run(ctx context.Context) error {
	if err := r.Reload(ctx); err != nil {
		return err
	}
	r.mu.Lock()
	c := r.cron
	r.mu.Unlock()
	c.Start()
	r.log.Info("schedule runner started")

	<-ctx.Done()
	stopCtx := c.Stop()
	<-stopCtx.Done()
	r.log.Info("schedule runner stopped")
	return nil
}

// Reload rebuilds the cron registration set from the database. Safe to call
// at any time — the old cron is stopped and a new one swapped in atomically
// under the mutex. The handler layer calls this after every Create / Update
// / Delete so changes take effect immediately.
func (r *Runner) Reload(ctx context.Context) error {
	rows, err := r.svc.repo.ListEnabled(ctx)
	if err != nil {
		return err
	}

	fresh := cron.New() // 5-field; matches schedule.parser.

	// Track which schedules registered successfully — schedules whose cron
	// expression fails to parse are skipped, so we can't index back into rows
	// positionally.
	registered := make([]*Schedule, 0, len(rows))

	for _, sch := range rows {
		// Capture id by value so the closure doesn't share the loop variable.
		id := sch.ID
		if _, err := fresh.AddFunc(sch.CronExpr, func() {
			r.fire(id)
		}); err != nil {
			r.log.Warn("schedule: skip invalid cron",
				"schedule_id", id, "expr", sch.CronExpr, "err", err)
			continue
		}
		registered = append(registered, sch)
	}

	r.mu.Lock()
	old := r.cron
	r.cron = fresh
	r.mu.Unlock()

	// If the old cron was already running, stop it and start the fresh one.
	// On the first Reload (from Run) old is nil, and Run calls Start itself.
	if old != nil {
		stopCtx := old.Stop()
		<-stopCtx.Done()
		fresh.Start()
	}

	// Persist next_run_at so the UI can show "next run in 32s" without
	// needing the cron parser client-side. We compute Next directly off the
	// parser rather than fresh.Entry().Next — the latter is only populated
	// once the cron's run loop has iterated, which is racy here (nothing
	// has fired yet on the very first Reload).
	now := time.Now()
	for _, sch := range registered {
		schedule, err := parser.Parse(sch.CronExpr)
		if err != nil {
			// Already passed AddFunc above; should be unreachable.
			continue
		}
		next := schedule.Next(now)
		if err := r.svc.repo.UpdateNextRun(ctx, sch.ID, &next); err != nil {
			r.log.Warn("schedule: update next_run_at failed",
				"schedule_id", sch.ID, "err", err)
		}
	}
	return nil
}

// fire is the cron-thread callback. Runs in its own goroutine per the cron
// lib's contract; we use a fresh background context (the one passed to
// Reload may have been short-lived).
func (r *Runner) fire(scheduleID int64) {
	ctx := context.Background()

	sch, err := r.svc.Get(ctx, scheduleID)
	if err != nil {
		r.log.Error("schedule: lookup failed", "schedule_id", scheduleID, "err", err)
		return
	}
	if !sch.Enabled {
		// Belt-and-braces: the cron lib should have been re-registered, but
		// races during Reload are possible.
		return
	}

	t, err := r.queuer.Queue(ctx, task.CreateInput{
		SpiderID:      sch.SpiderID,
		Trigger:       task.TriggerSchedule,
		TriggeredArgs: sch.Args,
	})
	if err != nil {
		r.log.Error("schedule: queue task failed",
			"schedule_id", scheduleID, "err", err)
		return
	}

	// Compute the next run from the schedule's cron expression directly —
	// the cron lib's Entry.Next is updated only when the entry fires, and
	// we want the post-fire next-run timestamp persisted now.
	var next *time.Time
	if sched, err := parser.Parse(sch.CronExpr); err == nil {
		n := sched.Next(time.Now())
		next = &n
	}
	now := time.Now()
	if err := r.svc.MarkRun(ctx, scheduleID, now, t.ID, next); err != nil {
		r.log.Warn("schedule: mark run failed",
			"schedule_id", scheduleID, "err", err)
	}
}

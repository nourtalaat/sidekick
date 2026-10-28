package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/retention"
)

// registerRetention registers the retention job, which deletes old records
// once a day as the retention section says, and the task that schedules
// it in every replica.
func registerRetention(a *app.App) error {
	metrics, err := retention.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return fmt.Errorf("set up the retention metrics: %w", err)
	}

	log := a.Logger().With("component", "retention")
	r := a.Config().Retention

	handler := retention.New(retention.Options{
		Pool: a.Pool(),
		Periods: retention.Periods{
			Events:         r.Events.Duration(),
			Investigations: r.Investigations.Duration(),
			Steps:          r.Steps.Duration(),
			AuditLog:       r.AuditLog.Duration(),
			WatcherChecks:  r.WatcherChecks.Duration(),
			MemoryEntries:  r.Memory.Duration(),
		},
		Metrics: metrics,
		Logger:  log,
	})

	if err := a.RegisterJob(queue.KindRetention, handler, queue.KindOptions{Long: true}); err != nil {
		return fmt.Errorf("register the retention job: %w", err)
	}

	scheduler := retention.NewScheduler(retention.SchedulerOptions{Pool: a.Pool(), Logger: log})
	if err := a.RegisterTask("retention-schedule", scheduler.Run); err != nil {
		return fmt.Errorf("register the retention schedule: %w", err)
	}

	return nil
}

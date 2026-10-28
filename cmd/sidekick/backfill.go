package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/backfill"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// registerBackfill registers the job that fills the investigation list's
// columns on investigations from before they existed, and the task that
// enqueues it at start-up while any are left.
func registerBackfill(a *app.App) error {
	log := a.Logger().With("component", "backfill")

	handler := backfill.New(backfill.Options{Pool: a.Pool(), Logger: log})

	if err := a.RegisterJob(queue.KindBackfill, handler, queue.KindOptions{Long: true}); err != nil {
		return fmt.Errorf("register the backfill job: %w", err)
	}

	scheduler := backfill.NewScheduler(backfill.SchedulerOptions{Pool: a.Pool(), Logger: log})
	if err := a.RegisterTask("backfill-schedule", scheduler.Run); err != nil {
		return fmt.Errorf("register the backfill schedule: %w", err)
	}

	return nil
}

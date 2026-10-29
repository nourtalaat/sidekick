package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/rollup"
)

// registerRollup registers the rollup job, which keeps the dashboard's
// tables up to date, and the task that enqueues it every minute in every
// replica.
func registerRollup(a *app.App) error {
	metrics, err := rollup.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return fmt.Errorf("set up the rollup metrics: %w", err)
	}

	log := a.Logger().With("component", "rollup")

	handler := rollup.New(rollup.Options{Pool: a.Pool(), Metrics: metrics, Logger: log})

	if err := a.RegisterJob(queue.KindRollup, handler, queue.KindOptions{Long: true}); err != nil {
		return fmt.Errorf("register the rollup job: %w", err)
	}

	scheduler := rollup.NewScheduler(rollup.SchedulerOptions{Pool: a.Pool(), Logger: log})
	if err := a.RegisterTask("rollup-schedule", scheduler.Run); err != nil {
		return fmt.Errorf("register the rollup schedule: %w", err)
	}

	return nil
}

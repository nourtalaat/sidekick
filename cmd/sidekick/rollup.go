package main

import (
	"fmt"
	"log/slog"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/rollup"
)

// registerRollup registers the rollup job, which keeps the dashboard's
// tables up to date, the task that enqueues it every minute in every
// replica, and the task that exports the dashboard's numbers as metrics in
// every replica.
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

	return registerDashboardMetrics(a, log)
}

// registerDashboardMetrics registers the task that reads the dashboard's
// numbers from the rollup tables once a minute and serves them on /metrics.
func registerDashboardMetrics(a *app.App, log *slog.Logger) error {
	cfg := a.Config()

	loc, err := cfg.Budgets.Daily.Location()
	if err != nil {
		return fmt.Errorf("set up the daily caps for the dashboard metrics: %w", err)
	}

	routeCaps := map[string]int64{}

	for _, r := range cfg.Routes {
		if micros := r.Budgets.Daily.MaxCostMicros(); micros > 0 {
			routeCaps[r.Name] = micros
		}
	}

	dashboard, err := rollup.NewDashboard(a.Telemetry().Registry(), rollup.DashboardOptions{
		Pool:            a.Pool(),
		Location:        loc,
		RouteCapsMicros: routeCaps,
		GlobalCapMicros: cfg.Budgets.Daily.MaxCostMicros(),
		Logger:          log,
	})
	if err != nil {
		return fmt.Errorf("set up the dashboard metrics: %w", err)
	}

	if err := a.RegisterTask("dashboard-metrics", dashboard.Run); err != nil {
		return fmt.Errorf("register the dashboard metrics: %w", err)
	}

	return nil
}

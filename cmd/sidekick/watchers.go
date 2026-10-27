package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/watch"
	"github.com/nourtalaat/sidekick/internal/watch/safehttp"
)

// registerWatchers runs the watch_tick jobs of every watcher, with HTTP
// checks through the safe client and tool checks through guard, and
// returns the starter of the start_watcher tool.
func registerWatchers(a *app.App, guard *tools.Guard) (*watch.ModelStarter, error) {
	checker, err := newChecker(a, guard)
	if err != nil {
		return nil, fmt.Errorf("set up the watcher checks: %w", err)
	}

	policy := a.Config().WatchPolicy()
	log := a.Logger().With("component", "watch")

	ticker, err := watch.NewTicker(watch.TickerOptions{
		Pool:       a.Pool(),
		Checker:    checker,
		Policy:     policy,
		CloseGroup: grouper.EnqueueClose,
		Logger:     log,
	})
	if err != nil {
		return nil, fmt.Errorf("set up the watcher ticks: %w", err)
	}

	if err := a.RegisterJob(queue.KindWatchTick, ticker, queue.KindOptions{}); err != nil {
		return nil, fmt.Errorf("register the watcher ticks: %w", err)
	}

	starter, err := watch.NewModelStarter(watch.ModelStarterOptions{
		Pool:    a.Pool(),
		Checker: checker,
		Policy:  policy,
		Logger:  log,
	})
	if err != nil {
		return nil, fmt.Errorf("set up start_watcher: %w", err)
	}

	return starter, nil
}

// newChecker builds the watcher check runner with the safe HTTP client of
// watch.http, the check metrics and the alert states in the database.
func newChecker(a *app.App, guard *tools.Guard) (*watch.Checker, error) {
	reg := a.Telemetry().Registry()

	httpMetrics, err := safehttp.NewMetrics(reg)
	if err != nil {
		return nil, fmt.Errorf("set up HTTP check metrics: %w", err)
	}

	rules := a.Config().Watch.HTTP

	client, err := safehttp.New(safehttp.Options{AllowHosts: rules.AllowHosts, AllowPrivate: rules.AllowPrivate, Metrics: httpMetrics})
	if err != nil {
		return nil, fmt.Errorf("build the HTTP check client: %w", err)
	}

	metrics, err := watch.NewMetrics(reg)
	if err != nil {
		return nil, fmt.Errorf("set up check metrics: %w", err)
	}

	checker, err := watch.New(watch.Options{
		Guard:    guard,
		HTTP:     client,
		Redactor: a.Redactor(),
		Secrets:  a.Secrets(),
		Alerts:   watch.StoreAlerts{DB: a.Pool()},
		Metrics:  metrics,
	})
	if err != nil {
		return nil, fmt.Errorf("build the checker: %w", err)
	}

	return checker, nil
}

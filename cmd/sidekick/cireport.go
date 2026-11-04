package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// registerCIReport registers the job that reads the CI of the pull requests
// and merge requests the actions opened and reports the result in the
// investigation's thread. A config with no action that reports CI registers
// nothing.
func registerCIReport(a *app.App) error {
	metrics, err := gitchange.NewCIMetrics(a.Telemetry().Registry())
	if err != nil {
		return fmt.Errorf("set up the CI report metrics: %w", err)
	}

	reporter := gitchange.NewCIReporter(gitchange.CIOptions{
		Pool:           a.Pool(),
		Actions:        a.Instances().Actions,
		Metrics:        metrics,
		Logger:         a.Logger().With("component", "ci-report"),
		TracerProvider: a.Telemetry().TracerProvider(),
	})

	if !reporter.Enabled() {
		return nil
	}

	if err := a.RegisterJob(queue.KindCIReport, reporter, queue.KindOptions{}); err != nil {
		return fmt.Errorf("register the CI report job: %w", err)
	}

	return nil
}

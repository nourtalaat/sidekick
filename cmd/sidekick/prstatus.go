package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/githubpr"
	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// registerPullRequestStatus registers the daily job that reads from GitHub
// whether the pull requests the github.pull_request actions opened were
// merged or closed, and the task that schedules it in every replica. A
// config with no such action registers nothing.
func registerPullRequestStatus(a *app.App) error {
	checker, err := githubpr.NewStatusChecker(githubpr.StatusOptions{
		Pool:           a.Pool(),
		Actions:        a.Instances().Actions,
		Registerer:     a.Telemetry().Registry(),
		Logger:         a.Logger().With("component", "pr-status"),
		TracerProvider: a.Telemetry().TracerProvider(),
	})
	if err != nil {
		return fmt.Errorf("set up the pull request status job: %w", err)
	}

	if !checker.Enabled() {
		return nil
	}

	if err := a.RegisterJob(queue.KindCheckPullRequests, queue.HandlerFunc(checker.Handle), queue.KindOptions{Long: true}); err != nil {
		return fmt.Errorf("register the pull request status job: %w", err)
	}

	scheduler := githubpr.NewScheduler(githubpr.SchedulerOptions{Pool: a.Pool(), Logger: a.Logger().With("component", "pr-status")})
	if err := a.RegisterTask("pr-status-schedule", scheduler.Run); err != nil {
		return fmt.Errorf("register the pull request status schedule: %w", err)
	}

	return nil
}

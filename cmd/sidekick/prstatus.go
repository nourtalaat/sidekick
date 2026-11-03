package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// registerPullRequestStatus registers the daily job that reads from the Git
// host whether the pull requests the actions opened, such as
// github.pull_request, were merged or closed, and the task that schedules it
// in every replica. A config with no such action registers nothing.
func registerPullRequestStatus(a *app.App) error {
	checker := gitchange.NewStatusChecker(gitchange.StatusOptions{
		Pool:           a.Pool(),
		Actions:        a.Instances().Actions,
		Logger:         a.Logger().With("component", "pr-status"),
		TracerProvider: a.Telemetry().TracerProvider(),
	})

	if !checker.Enabled() {
		return nil
	}

	if err := a.RegisterJob(queue.KindCheckPullRequests, queue.HandlerFunc(checker.Handle), queue.KindOptions{Long: true}); err != nil {
		return fmt.Errorf("register the pull request status job: %w", err)
	}

	scheduler := gitchange.NewScheduler(gitchange.SchedulerOptions{Pool: a.Pool(), Logger: a.Logger().With("component", "pr-status")})
	if err := a.RegisterTask("pr-status-schedule", scheduler.Run); err != nil {
		return fmt.Errorf("register the pull request status schedule: %w", err)
	}

	return nil
}

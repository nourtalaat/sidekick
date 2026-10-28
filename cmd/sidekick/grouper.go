package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// registerGrouper registers the close_group jobs, which close each event
// group when its window ends.
func registerGrouper(a *app.App) error {
	recorder, err := newWatchRecorder(a)
	if err != nil {
		return fmt.Errorf("set up the group closer: %w", err)
	}

	closer := grouper.NewCloser(grouper.CloserOptions{
		Pool:     a.Pool(),
		Logger:   a.Logger().With("component", "grouper"),
		Watchers: recorder,
	})

	if err := a.RegisterJob(queue.KindCloseGroup, closer, queue.KindOptions{}); err != nil {
		return fmt.Errorf("register the group closer: %w", err)
	}

	return nil
}

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
	closer := grouper.NewCloser(grouper.CloserOptions{
		Pool:   a.Pool(),
		Logger: a.Logger().With("component", "grouper"),
	})

	if err := a.RegisterJob(queue.KindCloseGroup, closer, queue.KindOptions{}); err != nil {
		return fmt.Errorf("register the group closer: %w", err)
	}

	return nil
}

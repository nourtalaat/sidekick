package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/router"
)

// registerRouter runs the route_event jobs that intake enqueues for each
// new event.
func registerRouter(a *app.App) error {
	r, err := router.New(router.Options{
		Config:         a.Config(),
		ModelSettings:  a.Instances().ModelSettings,
		Pool:           a.Pool(),
		Logger:         a.Logger().With("component", "router"),
		Metrics:        a.Telemetry().Registry(),
		TracerProvider: a.Telemetry().TracerProvider(),
	})
	if err != nil {
		return fmt.Errorf("set up the router: %w", err)
	}

	if err := a.RegisterJob(queue.KindRouteEvent, r, queue.KindOptions{}); err != nil {
		return fmt.Errorf("register the router: %w", err)
	}

	return nil
}

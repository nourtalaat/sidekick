package main

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/router"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// registerRouter runs the route_event jobs that intake enqueues for each
// new event. When a source is a service catalog, it also reads the
// catalogs in the background, so related services can share a group. The
// router takes the routes in effect from cache on every reload.
func registerRouter(a *app.App, cache *rules.Cache) error {
	catalog := grouper.NewCatalog(a.Instances().Sources, a.Logger().With("component", "catalog"))

	r, err := router.New(router.Options{
		Config:         a.Config(),
		ModelSettings:  a.Instances().ModelSettings,
		Pool:           a.Pool(),
		Logger:         a.Logger().With("component", "router"),
		Metrics:        a.Telemetry().Registry(),
		TracerProvider: a.Telemetry().TracerProvider(),
		Catalog:        catalog,
	})
	if err != nil {
		return fmt.Errorf("set up the router: %w", err)
	}

	if err := r.SetRoutes(cache.Current().RouteConfigs()); err != nil {
		return fmt.Errorf("use the runtime routes: %w", err)
	}

	cache.Subscribe(func(_ context.Context, e *rules.Effective) error {
		if err := r.SetRoutes(e.RouteConfigs()); err != nil {
			return fmt.Errorf("apply the runtime routes: %w", err)
		}

		return nil
	})

	if err := a.RegisterJob(queue.KindRouteEvent, r, queue.KindOptions{}); err != nil {
		return fmt.Errorf("register the router: %w", err)
	}

	if catalog.Empty() {
		return nil
	}

	readCatalogs := func(ctx context.Context) error {
		catalog.Run(ctx)

		return nil
	}

	if err := a.RegisterTask("catalog", readCatalogs); err != nil {
		return fmt.Errorf("register the service catalog reader: %w", err)
	}

	return nil
}

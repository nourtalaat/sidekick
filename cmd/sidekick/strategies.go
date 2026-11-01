package main

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

// registerStrategies loads the runtime strategies once, so the router and
// the investigator start with them, and keeps them current in a background
// task that reloads on every change and every minute. When the first load
// fails, for example because database.migrations is manual and the schema
// is not migrated yet, Sidekick starts with the built-in and file
// strategies and the task tries again.
func registerStrategies(ctx context.Context, a *app.App) (*strategystore.Cache, error) {
	log := a.Logger().With("component", "strategies")
	cache := strategystore.NewCache(strategystore.Options{Base: a.Config().StrategySet(), Pool: a.Pool(), Logger: log})

	if err := cache.Reload(ctx); err != nil {
		log.WarnContext(ctx, "loading the runtime strategies failed; starting with the built-in and file strategies", "error", err)
	}

	if err := a.RegisterTask("strategies-reload", cache.Run); err != nil {
		return nil, fmt.Errorf("register the runtime strategies reload: %w", err)
	}

	return cache, nil
}

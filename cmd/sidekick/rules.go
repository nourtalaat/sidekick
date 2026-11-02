package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/rules"
)

// registerRules loads the runtime listeners and routes once, so the router
// and the Slack app start with them, and keeps them current in a background
// task that reloads on every change and every minute. When the first load
// fails, for example because database.migrations is manual and the schema
// is not migrated yet, Sidekick starts with the file's rules and the task
// tries again.
func registerRules(ctx context.Context, a *app.App) (*rules.Cache, error) {
	log := a.Logger().With("component", "rules")
	cache := rules.NewCache(ctx, rules.Options{File: a.Config(), Pool: a.Pool(), Logger: log})

	if err := cache.Reload(ctx); err != nil {
		log.WarnContext(ctx, "loading the runtime rules failed; starting with the rules of the config file", "error", err)
	}

	warnUntrustedWrite(ctx, log, cache.Current())

	if err := a.RegisterTask("rules-reload", cache.Run); err != nil {
		return nil, fmt.Errorf("register the runtime rules reload: %w", err)
	}

	return cache, nil
}

// warnUntrustedWrite logs one warning for each read-write route in effect
// that sets allowUntrustedWrite, since its events can come from senders
// that are not trusted.
func warnUntrustedWrite(ctx context.Context, log *slog.Logger, effective *rules.Effective) {
	for _, r := range effective.Routes {
		if r.Config.Mode == action.ReadWrite && r.Config.AllowUntrustedWrite {
			log.WarnContext(ctx, "a read-write route runs actions for events from senders that are not trusted (allowUntrustedWrite)",
				"route", r.Config.Name, "layer", string(r.Layer))
		}
	}
}

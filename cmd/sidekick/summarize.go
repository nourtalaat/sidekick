package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

// summarizeOptions are the options of the caller of the connector name of
// the summarize role: its model settings, its prices, and the daily caps of
// the config, global and by route. Serve and the commands that call the
// role build their caller from it, so the caps are read in one place.
func summarizeOptions(cfg *config.Config, name string, conn model.Connector, settings model.Settings, prices pricing.Table) (summarize.Options, error) {
	loc, err := cfg.Budgets.Daily.Location()
	if err != nil {
		return summarize.Options{}, fmt.Errorf("set up the daily caps: %w", err)
	}

	routeCaps := make(map[string]int64)

	for _, r := range cfg.Routes {
		if limit := r.Budgets.Daily.MaxCostMicros(); limit > 0 {
			routeCaps[r.Name] = limit
		}
	}

	return summarize.Options{
		Connector: conn,
		Name:      name,
		Model:     settings.Model,
		Retries:   settings.Retries,
		Prices:    prices,
		Daily:     summarize.Daily{Location: loc, MaxCostMicros: cfg.Budgets.Daily.MaxCostMicros(), RouteMicros: routeCaps},
	}, nil
}

package main

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/model/instrument"
	"github.com/nourtalaat/sidekick/internal/scenario"
	"github.com/nourtalaat/sidekick/internal/scrub"
)

// newScenarioService builds the service the API exports, edits and reviews
// benchmark scenarios with. Its rubric items come from the connector of
// model.roles.summarize; without that role the drafts have no rubric
// items, which is logged once here.
func newScenarioService(ctx context.Context, a *app.App, modelMetrics *instrument.Metrics) (*scenario.Service, error) {
	cfg := a.Config()
	log := a.Logger().With("component", "scenarios")

	redaction, err := cfg.Redaction.RedactOptions(ctx, a.Secrets())
	if err != nil {
		return nil, fmt.Errorf("read the redaction settings: %w", err)
	}

	cleaner, err := scrub.New(scrub.Options{HashKey: redaction.HashKey, Custom: redaction.Custom, Logger: log})
	if err != nil {
		return nil, fmt.Errorf("set up the scenario cleaner: %w", err)
	}

	caller, err := newSummarizer(a, modelMetrics)
	if err != nil {
		return nil, err
	}

	if !caller.Enabled() {
		log.InfoContext(ctx, "model.roles.summarize is not set, so scenario drafts have no rubric items; the reviewer writes them")
	}

	return scenario.NewService(scenario.Options{
		Pool: a.Pool(), Cleaner: cleaner, Rubric: scenario.NewRubricDrafter(caller), Audit: audit.NewWriter(),
		Webhooks: cfg.Intake.Webhooks, Sources: cfg.Sources,
	}), nil
}

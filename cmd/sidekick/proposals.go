package main

import (
	"context"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/gate"
	"github.com/nourtalaat/sidekick/internal/model/instrument"
	"github.com/nourtalaat/sidekick/internal/proposals"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// proposalTrigger returns the trigger that makes a strategy version with
// enough wrong feedback get a proposed change. It is off unless
// strategies.proposals.enabled is true, and also off, with a line in the
// log, when no model fills the summarize role, because nothing could draft
// the change.
func proposalTrigger(ctx context.Context, a *app.App) proposals.Trigger {
	p := a.Config().Strategies.Proposals
	if !p.Enabled {
		return proposals.Trigger{}
	}

	if !summarizeConfigured(a.Config()) {
		a.Logger().WarnContext(ctx, "strategy proposals are enabled, but model.roles.summarize is not set, so no proposal will be drafted")

		return proposals.Trigger{}
	}

	return proposals.Trigger{Enabled: true, Threshold: p.WrongThreshold, Window: p.Window.Duration()}
}

// registerProposals registers the jobs that draft a proposed change to a
// strategy and run its benchmark gate. They are registered only when
// strategies.proposals.enabled is true and a summarize role is set; a job
// queued before that stays queued, and spends nothing. The gate runs the
// scenarios in bench.gate.publicDir with the reviewed ones.
func registerProposals(ctx context.Context, a *app.App, regs config.Registries, strategies strategy.Source, modelMetrics *instrument.Metrics) error {
	cfg := a.Config()
	if !cfg.Strategies.Proposals.Enabled || !summarizeConfigured(cfg) {
		return nil
	}

	log := a.Logger().With("component", "proposals")

	caller, err := newSummarizer(a, modelMetrics)
	if err != nil {
		return fmt.Errorf("set up the summarize role: %w", err)
	}

	draft := proposals.NewDraftJob(proposals.DraftOptions{
		Pool:       a.Pool(),
		Caller:     caller,
		Strategies: strategies,
		Redactor:   a.Redactor(),
		Logger:     log,
		Window:     cfg.Strategies.Proposals.Window.Duration(),
	})

	if err := a.RegisterJob(queue.KindProposeStrategy, draft, queue.KindOptions{Long: true, OnDead: draft.OnDead}); err != nil {
		return fmt.Errorf("register the strategy drafts: %w", err)
	}

	loc, err := cfg.Budgets.Daily.Location()
	if err != nil {
		return fmt.Errorf("set up the daily caps: %w", err)
	}

	g, err := gate.New(gate.Options{
		Pool:            a.Pool(),
		Config:          cfg,
		Registries:      regs,
		DatabaseURL:     func(ctx context.Context) (string, error) { return cfg.DatabaseURL(ctx, a.Secrets()) },
		Strategies:      strategies,
		Scenarios:       gate.MergeScenarios(log, gate.FileScenarios(cfg.Bench.Gate.PublicDir, cfg.Bench.Gate.BaseDir)),
		Daily:           gate.Daily{Location: loc, MaxCostMicros: cfg.Budgets.Daily.MaxCostMicros()},
		Logger:          log,
		ComponentLogger: a.Logger().With("component", "gate-bench"),
	})
	if err != nil {
		return fmt.Errorf("set up the benchmark gate: %w", err)
	}

	if err := a.RegisterJob(queue.KindBenchGate, g, queue.KindOptions{Long: true, Concurrency: 1, OnDead: g.OnDead}); err != nil {
		return fmt.Errorf("register the benchmark gate: %w", err)
	}

	log.InfoContext(ctx, "strategy proposals are on: a strategy version with enough wrong ratings gets a drafted change, and a benchmark gate checks it",
		"wrongThreshold", cfg.Strategies.Proposals.WrongThreshold, "window", cfg.Strategies.Proposals.Window.Duration().String(),
		"minRepeats", cfg.Bench.Gate.MinRepeats, "maxCostUSD", cfg.Bench.Gate.MaxCostUSD, "publicScenarios", cfg.Bench.Gate.PublicDir != "")

	return nil
}

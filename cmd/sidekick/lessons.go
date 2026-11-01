package main

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/lessons"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/instrument"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/summarize"
)

// registerLessons registers the job that drafts lessons from feedback. The
// draft is a call of the summarize role; without that role no lesson is
// drafted, and Sidekick logs that once, here. Admins can still write
// lessons by hand.
func registerLessons(a *app.App, modelMetrics *instrument.Metrics) error {
	cfg := a.Config()
	log := a.Logger().With("component", "lessons")

	caller, err := newSummarizer(a, modelMetrics)
	if err != nil {
		return fmt.Errorf("set up the summarize role: %w", err)
	}

	if !caller.Enabled() {
		log.Info("model.roles.summarize is not set, so no lesson is drafted from feedback; admins can still write lessons")
	}

	metrics, err := lessons.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return fmt.Errorf("set up lesson metrics: %w", err)
	}

	job := lessons.NewDraftJob(lessons.DraftOptions{
		Pool:            a.Pool(),
		Caller:          caller,
		Strategies:      cfg.StrategySet(),
		Redactor:        a.Redactor(),
		RequireApproval: cfg.Lessons.RequireApproval,
		Logger:          log,
		Metrics:         metrics,
	})

	if err := a.RegisterJob(queue.KindDraftLesson, job, queue.KindOptions{Long: true}); err != nil {
		return fmt.Errorf("register the lesson drafts: %w", err)
	}

	return nil
}

// newSummarizer builds the caller of the summarize role from model.roles.
// It is off when the role is not set. The connector is wrapped with its
// span and metrics, and its calls are priced with the same table as the
// investigator's and counted under the same daily caps.
func newSummarizer(a *app.App, modelMetrics *instrument.Metrics) (*summarize.Caller, error) {
	cfg := a.Config()
	log := a.Logger().With("component", "summarize")

	name := cfg.Model.Roles[model.RoleSummarize]

	conn, ok := a.Instances().Models[name]
	if name == "" || !ok {
		return summarize.New(summarize.Options{Logger: log}), nil
	}

	prices := pricing.New(cfg.Model.PriceTable())

	wrapped := instrument.Wrap(conn, instrument.Options{
		Connector: name,
		Provider:  cfg.Model.Connectors[name].Type,
		Metrics:   modelMetrics,
		Prices:    prices,
		Tracer:    a.Telemetry().TracerProvider().Tracer(instrument.TracerName),
	})

	opts, err := summarizeOptions(cfg, name, wrapped, a.Instances().ModelSettings[name], prices)
	if err != nil {
		return nil, err
	}

	opts.Logger = log

	return summarize.New(opts), nil
}

// summarizeConfigured reports whether model.roles.summarize is set, which
// is when feedback with a reason drafts a lesson.
func summarizeConfigured(cfg *config.Config) bool {
	return cfg.Model.Roles[model.RoleSummarize] != ""
}

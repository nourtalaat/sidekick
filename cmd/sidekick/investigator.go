package main

import (
	"fmt"
	"maps"
	"slices"

	"github.com/nourtalaat/sidekick/internal/app"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model/instrument"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// registerInvestigator runs the investigate jobs that the router enqueues
// for each new investigation, with every source behind the tool guard and
// every model connector wrapped with its span and metrics.
func registerInvestigator(a *app.App) error {
	guard, err := newGuard(a)
	if err != nil {
		return fmt.Errorf("set up the tool guard: %w", err)
	}

	cfg := a.Config()
	prices := pricing.New(cfg.Model.PriceTable())

	connectors, err := investigatorConnectors(a, prices)
	if err != nil {
		return fmt.Errorf("set up the model connectors: %w", err)
	}

	metrics, err := investigator.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return fmt.Errorf("set up investigation metrics: %w", err)
	}

	routes := make(map[string]investigator.Route, len(cfg.Routes))
	for _, r := range cfg.Routes {
		routes[r.Name] = investigator.Route{Sources: r.Sources}
	}

	h, err := investigator.New(investigator.Options{
		Pool:           a.Pool(),
		Guard:          guard,
		Connectors:     connectors,
		Prices:         prices,
		Strategies:     cfg.StrategySet(),
		Routes:         routes,
		Rules:          cfg.Report.Rules(),
		Logger:         a.Logger().With("component", "investigator"),
		Metrics:        metrics,
		TracerProvider: a.Telemetry().TracerProvider(),
	})
	if err != nil {
		return fmt.Errorf("set up the investigator: %w", err)
	}

	if err := a.RegisterJob(queue.KindInvestigate, h, queue.KindOptions{
		Concurrency: cfg.Budgets.Concurrency,
		Long:        true,
		OnDead:      h.OnDead,
	}); err != nil {
		return fmt.Errorf("register the investigator: %w", err)
	}

	return nil
}

// newGuard builds the tool guard for every configured source, with the
// tool call metrics and sidekick_source_up.
func newGuard(a *app.App) (*tools.Guard, error) {
	metrics, err := tools.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return nil, fmt.Errorf("set up tool metrics: %w", err)
	}

	in := a.Instances()

	sources := make([]tools.Source, 0, len(in.Sources))
	for _, name := range slices.Sorted(maps.Keys(in.Sources)) {
		sources = append(sources, tools.Source{Name: name, Source: in.Sources[name], Settings: in.SourceSettings[name]})
	}

	guard, err := tools.New(sources, tools.Options{
		Redactor: a.Redactor(),
		Logger:   a.Logger(),
		Metrics:  metrics,
		Tracer:   a.Telemetry().TracerProvider().Tracer(tools.TracerName),
	})
	if err != nil {
		return nil, fmt.Errorf("build the guard: %w", err)
	}

	return guard, nil
}

// investigatorConnectors wraps every built model connector with the
// model.generate span and the model metrics, priced with prices, the same
// table the investigator prices its steps with.
func investigatorConnectors(a *app.App, prices pricing.Table) (map[string]investigator.Connector, error) {
	metrics, err := instrument.NewMetrics(a.Telemetry().Registry())
	if err != nil {
		return nil, fmt.Errorf("set up model metrics: %w", err)
	}

	cfg := a.Config()
	tracer := a.Telemetry().TracerProvider().Tracer(instrument.TracerName)

	out := make(map[string]investigator.Connector, len(a.Instances().Models))

	for name, c := range a.Instances().Models {
		out[name] = investigator.Connector{
			Connector: instrument.Wrap(c, instrument.Options{
				Connector: name,
				Provider:  cfg.Model.Connectors[name].Type,
				Metrics:   metrics,
				Prices:    prices,
				Tracer:    tracer,
			}),
			Retries: a.Instances().ModelSettings[name].Retries,
		}
	}

	return out, nil
}

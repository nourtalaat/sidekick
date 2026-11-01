package gate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nourtalaat/sidekick/internal/bench"
)

// ScenarioSource supplies the scenarios a gate chooses from.
type ScenarioSource interface {
	// Scenarios returns every scenario the source has.
	Scenarios(ctx context.Context) ([]bench.Scenario, error)
}

// ScenarioFunc is a function that is a ScenarioSource.
type ScenarioFunc func(ctx context.Context) ([]bench.Scenario, error)

// Scenarios calls f.
func (f ScenarioFunc) Scenarios(ctx context.Context) ([]bench.Scenario, error) {
	return f(ctx)
}

// FileScenarios reads the scenario files under dir, whose snapshot paths are
// relative to baseDir. An empty dir has no scenarios.
func FileScenarios(dir, baseDir string) ScenarioSource {
	return ScenarioFunc(func(context.Context) ([]bench.Scenario, error) {
		if dir == "" {
			return nil, nil
		}

		scenarios, err := bench.LoadScenarios(dir, bench.LoadOptions{BaseDir: baseDir})
		if err != nil {
			return nil, fmt.Errorf("load the public scenarios from %s: %w", dir, err)
		}

		return scenarios, nil
	})
}

// MergeScenarios joins sources into one. A scenario whose ID an earlier source
// has is left out, with a warning, since results are told apart by ID.
func MergeScenarios(log *slog.Logger, sources ...ScenarioSource) ScenarioSource {
	return ScenarioFunc(func(ctx context.Context) ([]bench.Scenario, error) {
		var (
			out  []bench.Scenario
			seen = map[string]bool{}
		)

		for _, source := range sources {
			scenarios, err := source.Scenarios(ctx)
			if err != nil {
				return nil, fmt.Errorf("get scenarios: %w", err)
			}

			for _, sc := range scenarios {
				if seen[sc.ID] {
					log.WarnContext(ctx, "two scenarios have one ID, so the later one is left out of the gate", "scenario", sc.ID, "path", sc.Path)

					continue
				}

				seen[sc.ID] = true

				out = append(out, sc)
			}
		}

		return out, nil
	})
}

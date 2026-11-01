package gate

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/bench"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/strategy"
)

// choose returns the scenarios that cover the strategy, in ID order, each set
// to run the strategy under test. A scenario covers a strategy when it names
// it, or when it names none, or the default strategy, and the strategy's
// match fits its event. A scenario that is not valid is left out, with a
// warning, so one bad scenario does not stop every gate.
func (g *Gate) choose(ctx context.Context, a arms) ([]bench.Scenario, error) {
	all, err := g.opts.Scenarios.Scenarios(ctx)
	if err != nil {
		return nil, fmt.Errorf("get the scenarios: %w", err)
	}

	checker, err := g.runner(ctx, nil, a.baselineSet, bench.Hooks{}, nil)
	if err != nil {
		return nil, fmt.Errorf("build a runner to check the scenarios: %w", err)
	}

	var selected []bench.Scenario

	for _, sc := range all {
		named := cmp.Or(sc.Strategy, config.DefaultStrategy)
		sc.Strategy = a.name

		if problems := checker.Problems(ctx, sc); len(problems) > 0 {
			g.log.WarnContext(ctx, "a scenario is left out of the gate because it is not valid", "scenario", sc.ID, "problems", strings.Join(problems, "; "))

			continue
		}

		ev, err := checker.Event(ctx, sc)
		if err != nil {
			g.log.WarnContext(ctx, "a scenario is left out of the gate because its event cannot be read", "scenario", sc.ID, "error", err.Error())

			continue
		}

		if covers(named, a.baseline, ev) {
			selected = append(selected, sc)
		}
	}

	slices.SortFunc(selected, func(x, y bench.Scenario) int { return strings.Compare(x.ID, y.ID) })

	return selected, nil
}

// covers reports whether a scenario that names the strategy named, with the
// event ev, covers st.
func covers(named string, st strategy.Strategy, ev event.Event) bool {
	switch {
	case named == st.Name:
		return true
	case named != config.DefaultStrategy:
		return false
	}

	return st.Match.Fits(ev.AlertName, eventText(ev))
}

// eventText is the text a strategy's match is tried against: the title and
// the text of the event, as triage reads them.
func eventText(ev event.Event) string {
	return strings.Join(slices.DeleteFunc([]string{ev.Title, ev.Text}, func(s string) bool { return s == "" }), "\n")
}

// setHash returns a hash of the scenarios a gate runs: their IDs, payloads
// and expected verdicts. A gate that is tried again keeps the runs it
// finished only when the hash is the same.
func setHash(scenarios []bench.Scenario) (string, error) {
	h := sha256.New()

	for _, sc := range scenarios {
		body, err := sc.Body()
		if err != nil {
			return "", fmt.Errorf("hash scenario %s: %w", sc.ID, err)
		}

		if _, err := fmt.Fprintf(h, "%s\x00%s\x00%s\x00", sc.ID, body, sc.Expected.Verdict); err != nil {
			return "", fmt.Errorf("hash scenario %s: %w", sc.ID, err)
		}
	}

	return hex.EncodeToString(h.Sum(nil)), nil
}

// runner builds a benchmark runner for scenarios with the strategies set. The
// runner runs each scenario as many times as the gate's settings say.
func (g *Gate) runner(ctx context.Context, scenarios []bench.Scenario, set *strategy.Set, hooks bench.Hooks, budget *bench.Budget) (*bench.Runner, error) {
	r, err := bench.New(ctx, scenarios, bench.Options{
		Config:          g.opts.Config,
		Registries:      g.opts.Registries,
		Repeat:          g.opts.Config.Bench.Gate.MinRepeats,
		Logger:          g.log,
		ComponentLogger: g.opts.ComponentLogger,
		Strategies:      set,
		Hooks:           hooks,
		Budget:          budget,
	})
	if err != nil {
		return nil, fmt.Errorf("build the benchmark runner: %w", err)
	}

	return r, nil
}

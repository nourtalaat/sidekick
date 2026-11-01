//go:build integration

package bench

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// errStopped is the error the hooks of the tests stop a run with.
var errStopped = errors.New("stopped by the test")

// newRunnerWith is newRunner with options on top of the config.
func newRunnerWith(t *testing.T, conn *modeltest.Connector, repeat int, opts Options, scenarios ...Scenario) *Runner {
	t.Helper()

	cfg, regs := loadTestConfig(t, conn)
	opts.Config, opts.Registries, opts.Repeat = cfg, regs, repeat

	r, err := New(t.Context(), scenarios, opts)
	require.NoError(t, err)

	return r
}

// turnsFor returns the scripted turns of n runs of the sample scenario.
func turnsFor(n int) []modeltest.Turn {
	var turns []modeltest.Turn
	for range n {
		turns = append(turns, sampleTurns()...)
	}

	return turns
}

func TestRunSkipsTheRunsHooksKept(t *testing.T) {
	t.Parallel()

	const keptCost = 4321

	kept := RunResult{Run: 1, Status: "final", CostMicros: keptCost, Report: report.Report{Verdict: report.FalsePositive, StopReason: report.StopSubmitted}}

	conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, turnsFor(1)...)
	conn.ExpectAllUsed()

	var before, after []int

	budget := NewBudget(0)

	runner := newRunnerWith(t, conn, 2, Options{
		Budget: budget,
		Hooks: Hooks{
			Done: func(scenarioID string, run int) (RunResult, bool) {
				return kept, scenarioID == "checkout-pool-size" && run == 1
			},
			Before: func(_ context.Context, _ string, run int) error {
				before = append(before, run)

				return nil
			},
			After: func(_ context.Context, _ string, run RunResult) error {
				after = append(after, run.Run)

				return nil
			},
		},
	}, loadSample(t))

	results, err := runner.Run(t.Context(), inDefaultSchema(t))
	require.NoError(t, err)

	runs := results.Scenarios[0].Runs
	require.Len(t, runs, 2)
	require.Equal(t, kept, runs[0], "the kept run is used as it was")
	require.Equal(t, report.TruePositive, runs[1].Report.Verdict, "the second run ran")
	require.Equal(t, []int{2}, before, "Before is called for runs that run")
	require.Equal(t, []int{2}, after, "After is called for runs that run")
	require.Equal(t, int64(keptCost)+runs[1].CostMicros, budget.Spent(), "the kept run's cost counts")
}

func TestRunStopsWhenAHookFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		hooks Hooks
	}{
		{
			name: "Before",
			hooks: Hooks{Before: func(_ context.Context, _ string, run int) error {
				if run == 2 {
					return errStopped
				}

				return nil
			}},
		},
		{
			name: "After",
			hooks: Hooks{After: func(_ context.Context, _ string, run RunResult) error {
				if run.Run == 1 {
					return errStopped
				}

				return nil
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, turnsFor(1)...)
			conn.ExpectAllUsed()

			results, err := newRunnerWith(t, conn, 3, Options{Hooks: tt.hooks}, loadSample(t)).Run(t.Context(), inDefaultSchema(t))

			require.ErrorIs(t, err, errStopped)
			require.Len(t, results.Scenarios, 1)
		})
	}
}

func TestRunStopsWhenTheNextRunWouldPassTheBudget(t *testing.T) {
	t.Parallel()

	conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, turnsFor(1)...)
	conn.ExpectAllUsed()

	// The first run starts, since nothing is spent. A run costs more than a
	// micro-dollar, so the second would pass the cap.
	budget := NewBudget(1)

	results, err := newRunnerWith(t, conn, 3, Options{Budget: budget}, loadSample(t)).Run(t.Context(), inDefaultSchema(t))

	require.ErrorIs(t, err, ErrOverBudget)
	require.Len(t, results.Scenarios[0].Runs, 1)
	require.Positive(t, budget.Spent())
}

func TestRunWithoutOptionsIsUnchanged(t *testing.T) {
	t.Parallel()

	conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, turnsFor(2)...)
	conn.ExpectAllUsed()

	runner := newRunner(t, conn, 2, loadSample(t))
	require.Nil(t, runner.hooks.Done)
	require.Nil(t, runner.hooks.Before)
	require.Nil(t, runner.hooks.After)
	require.Nil(t, runner.budget)

	results, err := runner.Run(t.Context(), inDefaultSchema(t))
	require.NoError(t, err)
	require.Len(t, results.Scenarios[0].Runs, 2)
}

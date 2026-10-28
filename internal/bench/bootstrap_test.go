package bench

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/report"
)

func TestBootstrap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scenarios []ScenarioResult
		seed      uint64
		// want are the intervals of the metrics the test checks, by key.
		want map[string]Interval
	}{
		{
			// Each resample has 5 scenarios. With 3 of 5 right, a resample
			// has no right verdict about 1% of the time, at most 1 about 9%
			// of the time and 5 about 8% of the time, so of 1,000 resamples
			// the 25th and 26th smallest are 0.2 and the 25th and 26th
			// largest are 1.
			name: "a fixed seed gives a known interval",
			scenarios: []ScenarioResult{
				scenario("a", report.TruePositive, verdictRun(report.TruePositive)),
				scenario("b", report.TruePositive, verdictRun(report.TruePositive)),
				scenario("c", report.TruePositive, verdictRun(report.TruePositive)),
				scenario("d", report.TruePositive, verdictRun(report.Inconclusive)),
				scenario("e", report.TruePositive, verdictRun(report.Inconclusive)),
			},
			seed: DefaultSeed,
			want: map[string]Interval{
				"verdictAccuracy": {Lower: 0.2, Upper: 1},
				"overall":         {Lower: 0.2, Upper: 1},
			},
		},
		{
			name: "scenarios that score the same give an interval of one value",
			scenarios: []ScenarioResult{
				scenario("a", report.TruePositive, verdictRun(report.TruePositive), verdictRun(report.Inconclusive)),
				scenario("b", report.TruePositive, verdictRun(report.Inconclusive), verdictRun(report.TruePositive)),
			},
			seed: 42,
			want: map[string]Interval{
				"verdictAccuracy": {Lower: 0.5, Upper: 0.5},
				"tpPrecision":     {Lower: 1, Upper: 1},
				"tpRecall":        {Lower: 0.5, Upper: 0.5},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results := &Results{Scenarios: tt.scenarios}
			require.NoError(t, Score(t.Context(), results, ScoreOptions{Seed: tt.seed}))
			require.Equal(t, BootstrapResamples, results.Summary.Resamples)

			for key, want := range tt.want {
				got, ok := results.Summary.Intervals[key]
				require.True(t, ok, "an interval for %s", key)
				require.InDelta(t, want.Lower, got.Lower, shareDelta, key)
				require.InDelta(t, want.Upper, got.Upper, shareDelta, key)
			}

			_, ok := results.Summary.Intervals["fpPrecision"]
			require.False(t, ok, "no interval for a metric without a value")

			again := &Results{Scenarios: tt.scenarios}
			require.NoError(t, Score(t.Context(), again, ScoreOptions{Seed: tt.seed}))
			require.Equal(t, results.Summary.Intervals, again.Summary.Intervals, "the same seed gives the same intervals")
		})
	}
}

func TestBootstrapNothingToResample(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scenarios []ScenarioResult
		resamples int
	}{
		{name: "no scenarios", resamples: BootstrapResamples},
		{name: "no resamples", scenarios: []ScenarioResult{scenario("a", report.TruePositive, verdictRun(report.TruePositive))}, resamples: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Nil(t, bootstrap(tt.scenarios, DefaultWeights, DefaultSeed, tt.resamples))
		})
	}
}

func TestQuantile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		sorted []float64
		q      float64
		want   float64
	}{
		{name: "one value", sorted: []float64{0.4}, q: 0.025, want: 0.4},
		{name: "the lowest", sorted: []float64{1, 2, 3}, q: 0, want: 1},
		{name: "the highest", sorted: []float64{1, 2, 3}, q: 1, want: 3},
		{name: "on a value", sorted: []float64{1, 2, 3}, q: 0.5, want: 2},
		{name: "between two values", sorted: []float64{0, 10}, q: 0.25, want: 2.5},
		{name: "the 2.5th percentile of 1 to 5", sorted: []float64{1, 2, 3, 4, 5}, q: 0.025, want: 1.1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.InDelta(t, tt.want, quantile(tt.sorted, tt.q), shareDelta)
		})
	}
}

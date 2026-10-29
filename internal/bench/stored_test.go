package bench

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCompareMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		a, b       StoredSummary
		key        string
		wantA      *float64
		wantB      *float64
		wantChange *float64
		wantDiff   Difference
	}{
		{
			name:  "intervals that overlap",
			a:     StoredSummary{Metrics: Metrics{Overall: new(0.80)}, Intervals: map[string]Interval{"overall": {Lower: 0.7, Upper: 0.9}}},
			b:     StoredSummary{Metrics: Metrics{Overall: new(0.85)}, Intervals: map[string]Interval{"overall": {Lower: 0.8, Upper: 0.95}}},
			key:   "overall",
			wantA: new(0.80), wantB: new(0.85), wantChange: new(0.85 - 0.80), wantDiff: DifferenceNone,
		},
		{
			name:  "intervals that do not overlap",
			a:     StoredSummary{Metrics: Metrics{VerdictAccuracy: new(0.5)}, Intervals: map[string]Interval{"verdictAccuracy": {Lower: 0.4, Upper: 0.6}}},
			b:     StoredSummary{Metrics: Metrics{VerdictAccuracy: new(0.9)}, Intervals: map[string]Interval{"verdictAccuracy": {Lower: 0.8, Upper: 1}}},
			key:   "verdictAccuracy",
			wantA: new(0.5), wantB: new(0.9), wantChange: new(0.9 - 0.5), wantDiff: DifferenceClear,
		},
		{
			name:  "a run with no interval",
			a:     StoredSummary{Metrics: Metrics{Overall: new(0.5)}},
			b:     StoredSummary{Metrics: Metrics{Overall: new(0.6)}, Intervals: map[string]Interval{"overall": {Lower: 0.5, Upper: 0.7}}},
			key:   "overall",
			wantA: new(0.5), wantB: new(0.6), wantChange: new(0.6 - 0.5), wantDiff: DifferenceUnknown,
		},
		{
			name:     "a run with nothing to count",
			a:        StoredSummary{},
			b:        StoredSummary{Metrics: Metrics{RootCauseMatch: new(0.5)}},
			key:      "rootCauseMatch",
			wantB:    new(0.5),
			wantDiff: DifferenceUnknown,
		},
		{
			name:  "the mean cost has no interval",
			a:     StoredSummary{Metrics: Metrics{MeanCostPerRunUSD: new(0.02)}},
			b:     StoredSummary{Metrics: Metrics{MeanCostPerRunUSD: new(0.05)}},
			key:   KeyMeanCostPerRunUSD,
			wantA: new(0.02), wantB: new(0.05), wantChange: new(0.05 - 0.02), wantDiff: DifferenceUnknown,
		},
		{
			name:  "the mean time has no interval",
			a:     StoredSummary{Metrics: Metrics{MeanDurationMs: new(1000.0)}},
			b:     StoredSummary{Metrics: Metrics{MeanDurationMs: new(2500.0)}},
			key:   KeyMeanDurationMs,
			wantA: new(1000.0), wantB: new(2500.0), wantChange: new(1500.0), wantDiff: DifferenceUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got *MetricComparison

			for _, row := range CompareMetrics(tt.a, tt.b) {
				if row.Key == tt.key {
					got = &row

					break
				}
			}

			require.NotNil(t, got, "the metric has a row")
			require.Equal(t, tt.wantA, got.A)
			require.Equal(t, tt.wantB, got.B)
			require.Equal(t, tt.wantDiff, got.Difference)

			if tt.wantChange == nil {
				require.Nil(t, got.Change)

				return
			}

			require.NotNil(t, got.Change)
			require.InDelta(t, *tt.wantChange, *got.Change, 1e-9)
		})
	}
}

func TestCompareMetricsListsEveryRateMetric(t *testing.T) {
	t.Parallel()

	rows := CompareMetrics(StoredSummary{}, StoredSummary{})

	require.Len(t, rows, len(rateMetrics)+2)
	require.Equal(t, "overall", rows[0].Key)
	require.Equal(t, KeyMeanDurationMs, rows[len(rows)-1].Key)
}

func TestParseStoredSummary(t *testing.T) {
	t.Parallel()

	got, err := ParseStoredSummary([]byte(`{"promptVersion":"p1","metrics":{"runs":4,"overall":0.5},"intervals":{"overall":{"lower":0.4,"upper":0.6}}}`))
	require.NoError(t, err)
	require.Equal(t, "p1", got.PromptVersion)
	require.Equal(t, 4, got.Metrics.Runs)
	require.Equal(t, Interval{Lower: 0.4, Upper: 0.6}, got.Intervals["overall"])

	_, err = ParseStoredSummary([]byte(`{`))
	require.Error(t, err)
}

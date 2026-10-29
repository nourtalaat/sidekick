package feedback

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/output"
)

func TestMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		stored      []output.Rating
		wantCorrect float64
		wantWrong   float64
	}{
		{name: "nothing stored"},
		{name: "one of each", stored: []output.Rating{output.RatingCorrect, output.RatingWrong}, wantCorrect: 1, wantWrong: 1},
		{name: "two correct", stored: []output.Rating{output.RatingCorrect, output.RatingCorrect}, wantCorrect: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewRegistry()

			m, err := NewMetrics(reg)
			require.NoError(t, err)

			for _, r := range tt.stored {
				m.Stored(TargetVerdict, r)
			}

			require.InDelta(t, tt.wantCorrect, testutil.ToFloat64(m.feedback.WithLabelValues(TargetVerdict, "correct")), 0)
			require.InDelta(t, tt.wantWrong, testutil.ToFloat64(m.feedback.WithLabelValues(TargetVerdict, "wrong")), 0)

			count, err := testutil.GatherAndCount(reg, MetricFeedback)
			require.NoError(t, err)
			require.Equal(t, 2, count, "both ratings are exported from the start")
		})
	}
}

func TestMetricsOnOneRegistryShareACounter(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	first, err := NewMetrics(reg)
	require.NoError(t, err)

	second, err := NewMetrics(reg)
	require.NoError(t, err)

	first.Stored(TargetFinding, output.RatingWrong)
	second.Stored(TargetFinding, output.RatingWrong)

	require.InDelta(t, 2, testutil.ToFloat64(first.feedback.WithLabelValues(TargetFinding, "wrong")), 0)
}

func TestNilMetricsRecordNothing(t *testing.T) {
	t.Parallel()

	var m *Metrics

	require.NotPanics(t, func() { m.Stored(TargetVerdict, output.RatingCorrect) })
}

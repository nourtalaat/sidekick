package queue

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestMetricsSetCounts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		refresh [][]db.CountJobsRow
		want    map[[2]string]float64
	}{
		{
			name:    "one refresh",
			refresh: [][]db.CountJobsRow{{{Kind: KindPublish, Status: StatusPending, Jobs: 3}, {Kind: KindPublish, Status: StatusDead, Jobs: 1}}},
			want:    map[[2]string]float64{{KindPublish, StatusPending}: 3, {KindPublish, StatusDead}: 1},
		},
		{
			name: "a count that disappears goes to zero",
			refresh: [][]db.CountJobsRow{
				{{Kind: KindPublish, Status: StatusPending, Jobs: 3}, {Kind: KindRollup, Status: StatusRunning, Jobs: 1}},
				{{Kind: KindPublish, Status: StatusPending, Jobs: 2}},
				{},
			},
			want: map[[2]string]float64{{KindPublish, StatusPending}: 0, {KindRollup, StatusRunning}: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metrics, err := NewMetrics(prometheus.NewRegistry())
			require.NoError(t, err)

			for _, counts := range tt.refresh {
				metrics.setCounts(counts)
			}

			require.Equal(t, len(tt.want), testutil.CollectAndCount(metrics.jobs))

			for labels, want := range tt.want {
				require.InDelta(t, want, testutil.ToFloat64(metrics.jobs.WithLabelValues(labels[0], labels[1])), 0, "%v", labels)
			}
		})
	}
}

func TestMetricsObserveWait(t *testing.T) {
	t.Parallel()

	const lease = time.Minute

	runAt := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name        string
		lockedUntil *time.Time
		wantCount   uint64
		wantSum     float64
	}{
		{name: "waited three seconds", lockedUntil: new(runAt.Add(3*time.Second + lease)), wantCount: 1, wantSum: 3},
		{name: "claimed before run_at counts as no wait", lockedUntil: new(runAt.Add(lease - time.Second)), wantCount: 1, wantSum: 0},
		{name: "not claimed", wantCount: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metrics, err := NewMetrics(prometheus.NewRegistry())
			require.NoError(t, err)

			metrics.observeWait(db.Job{Kind: KindPublish, RunAt: runAt, LockedUntil: tt.lockedUntil}, lease)

			count, sum := histogramOf(t, metrics.wait, KindPublish)
			require.Equal(t, tt.wantCount, count)
			require.InDelta(t, tt.wantSum, sum, 0.001)
		})
	}
}

func TestMetricsJobDied(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		kinds []string
		want  map[string]float64
	}{
		{name: "counted by kind", kinds: []string{KindPublish, KindPublish, KindInvestigate}, want: map[string]float64{KindPublish: 2, KindInvestigate: 1}},
		{name: "nothing died", want: map[string]float64{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			metrics, err := NewMetrics(prometheus.NewRegistry())
			require.NoError(t, err)

			for _, kind := range tt.kinds {
				metrics.jobDied(kind)
			}

			require.Equal(t, len(tt.want), testutil.CollectAndCount(metrics.dead))

			for kind, want := range tt.want {
				require.InDelta(t, want, testutil.ToFloat64(metrics.dead.WithLabelValues(kind)), 0)
			}
		})
	}
}

func TestNilMetricsRecordNothing(t *testing.T) {
	t.Parallel()

	var metrics *Metrics

	require.NotPanics(t, func() {
		metrics.observeWait(db.Job{LockedUntil: new(time.Now())}, time.Minute)
		metrics.jobDied(KindPublish)
		metrics.setCounts([]db.CountJobsRow{{Kind: KindPublish, Status: StatusPending, Jobs: 1}})
	})
}

func TestNewMetricsRegistersOnce(t *testing.T) {
	t.Parallel()

	registry := prometheus.NewRegistry()

	_, err := NewMetrics(registry)
	require.NoError(t, err)

	_, err = NewMetrics(registry)
	require.ErrorContains(t, err, "register queue metrics")
}

// histogramOf returns the sample count and sum of the histogram with the
// given label values.
func histogramOf(t *testing.T, vec *prometheus.HistogramVec, labels ...string) (count uint64, sum float64) {
	t.Helper()

	histogram, ok := vec.WithLabelValues(labels...).(prometheus.Metric)
	require.True(t, ok)

	var out dto.Metric
	require.NoError(t, histogram.Write(&out))

	return out.GetHistogram().GetSampleCount(), out.GetHistogram().GetSampleSum()
}

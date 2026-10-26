package webhook

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
)

func TestMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record func(m *Metrics)
		// want is the value of each received and rejected series by
		// result or reason; series left out are zero.
		want map[string]float64
	}{
		{
			name:   "a new endpoint starts every series at zero",
			record: func(m *Metrics) { m.init("am") },
		},
		{
			name: "accepted and duplicate events count one each",
			record: func(m *Metrics) {
				m.stored("am", []storedEvent{{key: "a"}, {key: "b", duplicate: true}, {key: "c"}})
			},
			want: map[string]float64{resultAccepted: 2, resultDuplicate: 1},
		},
		{
			name:   "a rejection counts in both metrics",
			record: func(m *Metrics) { m.reject("am", reasonTooLarge) },
			want:   map[string]float64{resultRejected: 1, reasonTooLarge: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, err := NewMetrics(prometheus.NewRegistry())
			require.NoError(t, err)

			tt.record(m)

			for _, result := range []string{resultAccepted, resultDuplicate, resultRejected} {
				require.InDelta(t, tt.want[result], testutil.ToFloat64(m.received.WithLabelValues("am", result)), 0, result)
			}

			for _, reason := range []string{reasonUnauthenticated, reasonTooLarge, reasonInvalidPayload} {
				require.InDelta(t, tt.want[reason], testutil.ToFloat64(m.rejected.WithLabelValues("am", reason)), 0, reason)
			}
		})
	}
}

func TestMetricsRegistration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		twice   bool
		wantErr bool
	}{
		{name: "registers on a new registry"},
		{name: "fails when the metrics are already registered", twice: true, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := prometheus.NewRegistry()
			if tt.twice {
				_, err := NewMetrics(reg)
				require.NoError(t, err)
			}

			_, err := NewMetrics(reg)
			if tt.wantErr {
				require.ErrorContains(t, err, "register intake metrics")

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestNilMetricsRecordNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record func(m *Metrics)
	}{
		{name: "init", record: func(m *Metrics) { m.init("am") }},
		{name: "reject", record: func(m *Metrics) { m.reject("am", reasonTooLarge) }},
		{name: "stored", record: func(m *Metrics) { m.stored("am", []storedEvent{{key: "a"}}) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotPanics(t, func() { tt.record(nil) })
		})
	}
}

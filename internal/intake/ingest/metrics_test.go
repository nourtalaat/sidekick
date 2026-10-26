package ingest

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
			name:   "a new intake starts every series at zero",
			record: func(m *Metrics) { m.Init("am") },
		},
		{
			name: "accepted and duplicate events count one each",
			record: func(m *Metrics) {
				m.Stored("am", []Stored{{Key: "a"}, {Key: "b", Duplicate: true}, {Key: "c"}})
			},
			want: map[string]float64{ResultAccepted: 2, ResultDuplicate: 1},
		},
		{
			name:   "a rejection counts in both metrics",
			record: func(m *Metrics) { m.Reject("am", ReasonTooLarge) },
			want:   map[string]float64{ResultRejected: 1, ReasonTooLarge: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			m, err := NewMetrics(prometheus.NewRegistry())
			require.NoError(t, err)

			tt.record(m)

			for _, result := range []string{ResultAccepted, ResultDuplicate, ResultRejected} {
				require.InDelta(t, tt.want[result], testutil.ToFloat64(m.received.WithLabelValues("am", result)), 0, result)
			}

			for _, reason := range []string{ReasonUnauthenticated, ReasonTooLarge, ReasonInvalidPayload} {
				require.InDelta(t, tt.want[reason], testutil.ToFloat64(m.rejected.WithLabelValues("am", reason)), 0, reason)
			}
		})
	}
}

func TestMetricsRegistration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reg     func() prometheus.Registerer
		twice   bool
		wantErr bool
	}{
		{name: "registers on a new registry", reg: func() prometheus.Registerer { return prometheus.NewRegistry() }},
		{name: "a second intake shares the registered metrics", reg: func() prometheus.Registerer { return prometheus.NewRegistry() }, twice: true},
		{name: "a nil registry gives metrics that are not exported", reg: func() prometheus.Registerer { return nil }},
		{
			name: "fails when another collector has the name",
			reg: func() prometheus.Registerer {
				reg := prometheus.NewRegistry()
				reg.MustRegister(prometheus.NewGauge(prometheus.GaugeOpts{Name: "sidekick_events_received_total", Help: "x"}))

				return reg
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			reg := tt.reg()

			var first *Metrics

			if tt.twice {
				var err error
				first, err = NewMetrics(reg)
				require.NoError(t, err)
			}

			m, err := NewMetrics(reg)
			if tt.wantErr {
				require.ErrorContains(t, err, "register intake metrics")

				return
			}

			require.NoError(t, err)

			if first != nil {
				first.Reject("am", ReasonTooLarge)
				require.InDelta(t, 1, testutil.ToFloat64(m.rejected.WithLabelValues("am", ReasonTooLarge)), 0)
			}
		})
	}
}

func TestNilMetricsRecordNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		record func(m *Metrics)
	}{
		{name: "init", record: func(m *Metrics) { m.Init("am") }},
		{name: "reject", record: func(m *Metrics) { m.Reject("am", ReasonTooLarge) }},
		{name: "stored", record: func(m *Metrics) { m.Stored("am", []Stored{{Key: "a"}}) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NotPanics(t, func() { tt.record(nil) })
		})
	}
}

package rollup

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// labelTable is the label of the table whose buckets were counted.
const labelTable = "table"

// Metrics are the rollup job's Prometheus metrics. Pass them to New in
// Options.Metrics. A nil *Metrics records nothing.
type Metrics struct {
	buckets  *prometheus.CounterVec
	duration prometheus.Histogram
	dirty    prometheus.Gauge
}

// NewMetrics creates the rollup metrics and registers them with reg:
// sidekick_rollup_buckets_total, a counter by table of the buckets this
// replica counted again; sidekick_rollup_run_duration_seconds, a histogram
// of the time a run took; and sidekick_rollup_dirty_buckets, a gauge of the
// buckets that waited for a recount when the last run ended.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		buckets: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sidekick_rollup_buckets_total",
			Help: "Dashboard rollup buckets counted again, by table.",
		}, []string{labelTable}),
		duration: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "sidekick_rollup_run_duration_seconds",
			Help:    "How long a rollup run took.",
			Buckets: prometheus.DefBuckets,
		}),
		dirty: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "sidekick_rollup_dirty_buckets",
			Help: "Dashboard rollup buckets that waited for a recount when the last run on this replica ended.",
		}),
	}

	for _, c := range []prometheus.Collector{m.buckets, m.duration, m.dirty} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register rollup metrics: %w", err)
		}
	}

	return m, nil
}

// counted counts n buckets of table counted again.
func (m *Metrics) counted(table string, n int64) {
	if m == nil || n == 0 {
		return
	}

	m.buckets.WithLabelValues(table).Add(float64(n))
}

// ran records the time of a run.
func (m *Metrics) ran(d time.Duration) {
	if m == nil {
		return
	}

	m.duration.Observe(d.Seconds())
}

// waiting records how many buckets wait for a recount.
func (m *Metrics) waiting(n int64) {
	if m == nil {
		return
	}

	m.dirty.Set(float64(n))
}

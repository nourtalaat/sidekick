package watch

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric names.
const (
	MetricChecks        = "sidekick_watcher_checks_total"
	MetricCheckDuration = "sidekick_watcher_check_duration_seconds"
)

// Metric labels.
const (
	labelType   = "type"
	labelResult = "result"
)

// Duration buckets: checks take from a few milliseconds to the 10 second
// default timeout, so the buckets double from 5 milliseconds to about 10
// seconds.
const (
	durationFirstBucket = 0.005
	durationFactor      = 2
	durationBuckets     = 12
)

// Metrics are the check metrics. A nil *Metrics records nothing.
type Metrics struct {
	checks   *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

// recordCheck counts one check and its duration.
func (m *Metrics) recordCheck(t Type, r Result, elapsed time.Duration) {
	if m == nil {
		return
	}

	m.checks.WithLabelValues(string(t), string(r)).Inc()
	m.duration.WithLabelValues(string(t)).Observe(elapsed.Seconds())
}

// NewMetrics makes the check metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		checks: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricChecks,
			Help: "Watcher checks, by type (http, tool or alert) and result (pass, fail or error).",
		}, []string{labelType, labelResult}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    MetricCheckDuration,
			Help:    "Duration of watcher checks, by type.",
			Buckets: prometheus.ExponentialBuckets(durationFirstBucket, durationFactor, durationBuckets),
		}, []string{labelType}),
	}

	for _, c := range []prometheus.Collector{m.checks, m.duration} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register watcher check metrics: %w", err)
		}
	}

	return m, nil
}

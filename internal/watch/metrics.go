package watch

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Metric names.
const (
	MetricChecks        = "sidekick_watcher_checks_total"
	MetricCheckDuration = "sidekick_watcher_check_duration_seconds"
	MetricStateChanges  = "sidekick_watcher_state_changes_total"
	MetricRunning       = "sidekick_watchers_running"
)

// runningScrapeTimeout bounds the count of running watchers at a scrape.
const runningScrapeTimeout = 5 * time.Second

// Metric labels.
const (
	labelType   = "type"
	labelResult = "result"
	labelStatus = "status"
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

// runningCollector reports MetricRunning from the watchers table.
type runningCollector struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
	desc *prometheus.Desc
}

var _ prometheus.Collector = (*runningCollector)(nil)

// Describe sends the gauge's description.
func (c *runningCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect sends the number of running watchers that have not passed their
// deadline.
func (c *runningCollector) Collect(ch chan<- prometheus.Metric) {
	ctx, cancel := context.WithTimeout(context.Background(), runningScrapeTimeout)
	defer cancel()

	running, err := db.New(c.pool).CountRunningWatchers(ctx, c.now())
	if err != nil {
		c.log.WarnContext(ctx, "could not count the running watchers for the metrics", "error", err)

		return
	}

	ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, float64(running))
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

// RegisterRunning registers MetricRunning with reg: a gauge of the
// watchers running in the deployment, counted in pool at each scrape, so
// every replica reports the same number; combine replicas with max, not
// sum. A count that fails is logged to log and leaves the gauge out of
// that scrape.
func RegisterRunning(reg prometheus.Registerer, pool *pgxpool.Pool, log *slog.Logger) error {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	c := &runningCollector{
		pool: pool,
		log:  log,
		now:  time.Now,
		desc: prometheus.NewDesc(MetricRunning, "Watchers running in the deployment, read from the database.", nil, nil),
	}

	if err := reg.Register(c); err != nil {
		return fmt.Errorf("register %s: %w", MetricRunning, err)
	}

	return nil
}

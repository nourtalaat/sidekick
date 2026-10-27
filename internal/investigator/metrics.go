package investigator

import (
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Metric names.
const (
	MetricInvestigations  = "sidekick_investigations_total"
	MetricRunning         = "sidekick_investigations_running"
	MetricDuration        = "sidekick_investigation_duration_seconds"
	MetricFirstFinding    = "sidekick_time_to_first_finding_seconds"
	MetricBudgetExhausted = "sidekick_budget_exhausted_total"
	MetricEventsSkipped   = "sidekick_events_skipped_total"
)

// Metric labels.
const (
	labelRoute      = "route"
	labelVerdict    = "verdict"
	labelStopReason = "stop_reason"
	labelBudget     = "budget"
	labelReason     = "reason"
)

// Duration buckets: an investigation takes from a few seconds to its wall
// time, 10 minutes by default, so the buckets double from 5 seconds to
// about 40 minutes.
const (
	durationFirstBucket = 5
	durationFactor      = 2
	durationBuckets     = 10
)

// Metrics are the investigation metrics. A nil *Metrics records nothing.
type Metrics struct {
	investigations  *prometheus.CounterVec
	runningRuns     *prometheus.GaugeVec
	duration        *prometheus.HistogramVec
	firstFindings   *prometheus.HistogramVec
	budgetExhausted *prometheus.CounterVec
	skippedEvents   *prometheus.CounterVec
	// reg is where NewMetrics registered them, for the collectors New adds.
	reg prometheus.Registerer
}

// skipped counts the event of an investigation that is not run, for
// reason.
func (m *Metrics) skipped(route, reason string) {
	if m == nil {
		return
	}

	m.skippedEvents.WithLabelValues(route, reason).Inc()
}

// running counts one run of the route as running, until the returned
// function is called.
func (m *Metrics) running(route string) (done func()) {
	if m == nil {
		return func() {}
	}

	g := m.runningRuns.WithLabelValues(route)
	g.Inc()

	return g.Dec
}

// firstFinding records the time from the start of an investigation to its
// first finding.
func (m *Metrics) firstFinding(route string, elapsed time.Duration) {
	if m == nil {
		return
	}

	m.firstFindings.WithLabelValues(route).Observe(elapsed.Seconds())
}

// finished counts an ended investigation and records how long it took.
func (m *Metrics) finished(inv db.Investigation, end ending) {
	if m == nil {
		return
	}

	m.investigations.WithLabelValues(inv.Route, string(end.report.Verdict), string(end.report.StopReason)).Inc()

	if end.budget != "" {
		m.budgetExhausted.WithLabelValues(inv.Route, end.budget).Inc()
	}

	if inv.StartedAt != nil && inv.FinishedAt != nil {
		m.duration.WithLabelValues(inv.Route).Observe(inv.FinishedAt.Sub(*inv.StartedAt).Seconds())
	}
}

// register adds c to the registry of m.
func (m *Metrics) register(c prometheus.Collector) error {
	if m == nil {
		return nil
	}

	if err := m.reg.Register(c); err != nil {
		return fmt.Errorf("register investigation metrics: %w", err)
	}

	return nil
}

// NewMetrics makes the investigation metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	buckets := prometheus.ExponentialBuckets(durationFirstBucket, durationFactor, durationBuckets)

	m := &Metrics{
		reg: reg,
		investigations: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricInvestigations,
			Help: "Investigations that ended, by route, verdict and stop reason.",
		}, []string{labelRoute, labelVerdict, labelStopReason}),
		runningRuns: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: MetricRunning,
			Help: "Investigation runs going on in this replica, by route.",
		}, []string{labelRoute}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    MetricDuration,
			Help:    "Time from the start of an investigation to its end.",
			Buckets: buckets,
		}, []string{labelRoute}),
		firstFindings: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    MetricFirstFinding,
			Help:    "Time from the start of an investigation to its first recorded finding.",
			Buckets: buckets,
		}, []string{labelRoute}),
		budgetExhausted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricBudgetExhausted,
			Help: "Investigations that ended because a budget ran out, by route and budget: tokens, tool_calls, wall_time, cost or daily_cap.",
		}, []string{labelRoute, labelBudget}),
	}

	for _, c := range []prometheus.Collector{m.investigations, m.runningRuns, m.duration, m.firstFindings, m.budgetExhausted} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register investigation metrics: %w", err)
		}
	}

	skipped, err := NewSkippedEvents(reg)
	if err != nil {
		return nil, fmt.Errorf("register investigation metrics: %w", err)
	}

	m.skippedEvents = skipped

	return m, nil
}

// NewSkippedEvents returns sidekick_events_skipped_total registered with
// reg, or the one registered there already, so the router, which skips
// events over a rate limit, and the investigator, which skips them at a
// daily cap, count in the same metric.
func NewSkippedEvents(reg prometheus.Registerer) (*prometheus.CounterVec, error) {
	c, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricEventsSkipped,
		Help: "Events that are not investigated, by route and reason: rate_limit or daily_cap.",
	}, []string{labelRoute, labelReason}))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", MetricEventsSkipped, err)
	}

	return c, nil
}

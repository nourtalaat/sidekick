package tools

import (
	"fmt"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric names.
const (
	MetricCalls    = "sidekick_tool_calls_total"
	MetricDuration = "sidekick_tool_call_duration_seconds"
	MetricSourceUp = "sidekick_source_up"
	MetricKeysFull = "sidekick_guard_keys_full_total"
)

// SpanName is the name of the span of one tool call.
const SpanName = "tool.call"

// TracerName names the tracer of this package. Pass it to the tracer
// provider when setting Options.Tracer.
const TracerName = "github.com/nourtalaat/sidekick/internal/investigator/tools"

// Span attributes. Tool input and results never become attributes.
const (
	attrSource  = "sidekick.source"
	attrTool    = "sidekick.tool"
	attrOutcome = "sidekick.outcome"
)

// Metric labels.
const (
	labelSource  = "source"
	labelTool    = "tool"
	labelOutcome = "outcome"
)

// Outcomes of MetricCalls and of the sidekick.outcome span attribute. Calls
// that never reach a source have the outcomes invalid_input and
// unknown_tool on their span, and no metric.
const (
	outcomeOK          = "ok"
	outcomeNoData      = "no_data"
	outcomeError       = "error"
	outcomeTimeout     = "timeout"
	outcomeUnavailable = "unavailable"
)

// Duration buckets: source calls take from a few milliseconds to the 30
// second default timeout and a little past it, so the buckets double from
// 10 milliseconds to about 40 seconds.
const (
	durationFirstBucket = 0.01
	durationFactor      = 2
	durationBuckets     = 13
)

// Metrics are the tool call metrics. A nil *Metrics records nothing.
type Metrics struct {
	calls    *prometheus.CounterVec
	duration *prometheus.HistogramVec
	up       *upCollector
	keysFull prometheus.Counter
}

// recordCall counts one source call and its duration.
func (m *Metrics) recordCall(sourceName, tool, outcome string, elapsed time.Duration) {
	if m == nil {
		return
	}

	m.calls.WithLabelValues(sourceName, tool, outcome).Inc()
	m.duration.WithLabelValues(sourceName).Observe(elapsed.Seconds())
}

// recordKeysFull counts a Do call refused because every key was in use.
func (m *Metrics) recordKeysFull() {
	if m == nil {
		return
	}

	m.keysFull.Inc()
}

// watch makes MetricSourceUp report the sources of g.
func (m *Metrics) watch(g *Guard) {
	if m == nil {
		return
	}

	m.up.guard.Store(g)
}

// upCollector reports MetricSourceUp when the metrics are scraped: 1 for a
// source that is connected (when it can say) and whose circuit is closed,
// 0 otherwise.
type upCollector struct {
	desc  *prometheus.Desc
	guard atomic.Pointer[Guard]
}

// Describe implements prometheus.Collector.
func (c *upCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.desc
}

// Collect implements prometheus.Collector.
func (c *upCollector) Collect(ch chan<- prometheus.Metric) {
	g := c.guard.Load()
	if g == nil {
		return
	}

	for name, s := range g.sources {
		value := 0.0
		if s.up() {
			value = 1
		}

		ch <- prometheus.MustNewConstMetric(c.desc, prometheus.GaugeValue, value, name)
	}
}

// upReporter is implemented by sources that can say whether they are
// connected, such as MCP sources.
type upReporter interface {
	Up() bool
}

// up reports whether the source is connected, when it can say, and its
// circuit is closed.
func (s *guarded) up() bool {
	if r, ok := s.src.(upReporter); ok && !r.Up() {
		return false
	}

	return !s.breaker.open()
}

// NewMetrics makes the tool call metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricCalls,
			Help: "Source tool calls, by outcome: ok, no_data, error, timeout or unavailable.",
		}, []string{labelSource, labelTool, labelOutcome}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    MetricDuration,
			Help:    "Duration of source tool calls.",
			Buckets: prometheus.ExponentialBuckets(durationFirstBucket, durationFactor, durationBuckets),
		}, []string{labelSource}),
		up: &upCollector{desc: prometheus.NewDesc(
			MetricSourceUp,
			"Whether a source can take calls: 1 when it is connected and its circuit breaker is closed, 0 otherwise.",
			[]string{labelSource}, nil,
		)},
		keysFull: prometheus.NewCounter(prometheus.CounterOpts{
			Name: MetricKeysFull,
			Help: "Guarded calls that are not source tool calls, such as watcher HTTP probes, refused because the guard already held limits for its maximum number of targets, all in use.",
		}),
	}

	for _, c := range []prometheus.Collector{m.calls, m.duration, m.up, m.keysFull} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register tool metrics: %w", err)
		}
	}

	return m, nil
}

// outcomeOf returns the outcome label of a call that ended with err, or
// succeeded when err is nil.
func outcomeOf(err *ToolError, noData bool) string {
	if err == nil {
		if noData {
			return outcomeNoData
		}

		return outcomeOK
	}

	switch err.Kind {
	case KindTimeout:
		return outcomeTimeout
	case KindUnavailable:
		return outcomeUnavailable
	case KindInvalidInput, KindUnknownTool:
		return string(err.Kind)
	case KindToolError, KindFailed:
		return outcomeError
	default:
		return outcomeError
	}
}

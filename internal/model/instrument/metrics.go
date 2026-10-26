package instrument

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// Metric names.
const (
	MetricCalls    = "sidekick_model_calls_total"
	MetricDuration = "sidekick_model_call_duration_seconds"
	MetricTokens   = "sidekick_model_tokens_total"
	MetricCost     = "sidekick_model_cost_usd_total"
)

// Metric labels.
const (
	labelConnector = "connector"
	labelModel     = "model"
	labelRole      = "role"
	labelOutcome   = "outcome"
	labelKind      = "kind"
	labelRoute     = "route"
)

// Token kinds of MetricTokens.
const (
	tokensInput      = "input"
	tokensOutput     = "output"
	tokensCacheRead  = "cache_read"
	tokensCacheWrite = "cache_write"
)

// Duration buckets: model calls take from under a second to many minutes,
// so the buckets double from half a second to about 17 minutes.
const (
	durationFirstBucket = 0.5
	durationFactor      = 2
	durationBuckets     = 12
)

// Metrics are the model call metrics. A nil *Metrics records nothing.
type Metrics struct {
	calls    *prometheus.CounterVec
	duration *prometheus.HistogramVec
	tokens   *prometheus.CounterVec
	cost     *prometheus.CounterVec
}

// NewMetrics makes the model call metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		calls: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricCalls,
			Help: "Model calls, one per attempt, by outcome: ok or the error kind.",
		}, []string{labelConnector, labelModel, labelRole, labelOutcome}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    MetricDuration,
			Help:    "Duration of model calls, one per attempt.",
			Buckets: prometheus.ExponentialBuckets(durationFirstBucket, durationFactor, durationBuckets),
		}, []string{labelConnector, labelModel}),
		tokens: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricTokens,
			Help: "Tokens of successful model calls, by kind: input, output, cache_read, cache_write.",
		}, []string{labelConnector, labelModel, labelKind}),
		cost: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: MetricCost,
			Help: "Cost of successful model calls in US dollars, from the price table.",
		}, []string{labelConnector, labelModel, labelRoute}),
	}

	for _, c := range []prometheus.Collector{m.calls, m.duration, m.tokens, m.cost} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register model metrics: %w", err)
		}
	}

	return m, nil
}

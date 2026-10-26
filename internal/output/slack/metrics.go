package slack

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// The label names of sidekick_slack_api_errors_total.
const (
	labelMethod = "method"
	labelError  = "error"
)

// metrics are the Slack output's metrics. Every sink shares one set. A nil
// *metrics records nothing.
type metrics struct {
	errors *prometheus.CounterVec
}

// newMetrics creates sidekick_slack_api_errors_total, by Web API method and
// error, and registers it with reg, or takes the one another sink
// registered. The error is Slack's error code, such as channel_not_found,
// or ratelimited, request_failed, http_status, bad_response or other. A nil
// reg gives metrics that are not exported.
func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	counter, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_slack_api_errors_total",
		Help: "Failed Slack Web API calls by method and error.",
	}, []string{labelMethod, labelError}))
	if err != nil {
		return nil, fmt.Errorf("register Slack metrics: %w", err)
	}

	return &metrics{errors: counter}, nil
}

// apiError counts one failed call of method with code.
func (m *metrics) apiError(method, code string) {
	if m == nil {
		return
	}

	m.errors.WithLabelValues(method, code).Inc()
}

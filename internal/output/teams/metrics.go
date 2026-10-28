package teams

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// The label names of sidekick_teams_api_errors_total.
const (
	labelOperation = "operation"
	labelStatus    = "status"
)

// metrics are the Teams output's metrics. Every sink shares one set. A nil
// *metrics records nothing.
type metrics struct {
	errors *prometheus.CounterVec
}

// newMetrics creates sidekick_teams_api_errors_total, by Bot Framework
// Connector operation and status, and registers it with reg, or takes the
// one another sink registered. The status is the HTTP status, such as 429
// or 404, or request_failed or bad_response. A nil reg gives metrics that
// are not exported.
func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	counter, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_teams_api_errors_total",
		Help: "Failed Bot Framework Connector calls of the Teams output by operation and status.",
	}, []string{labelOperation, labelStatus}))
	if err != nil {
		return nil, fmt.Errorf("register Teams metrics: %w", err)
	}

	return &metrics{errors: counter}, nil
}

// apiError counts one failed call of op with status.
func (m *metrics) apiError(op, status string) {
	if m == nil {
		return
	}

	m.errors.WithLabelValues(op, status).Inc()
}

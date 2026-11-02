package githubpr

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// Labels of the metrics.
const (
	labelFormat = "format"
	labelResult = "result"
)

// Results of a validation.
const (
	resultPassed      = "passed"
	resultFailed      = "failed"
	resultUnavailable = "unavailable"
)

// Results of opening a pull request.
const (
	resultOpened   = "opened"
	resultExisting = "existing"
	resultError    = "error"
)

// metrics are the action's Prometheus metrics, shared by every instance.
type metrics struct {
	validations  *prometheus.CounterVec
	pullRequests *prometheus.CounterVec
}

// newMetrics creates the metrics and registers them with reg, which may be
// nil. Instances of the action share one set of collectors.
func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	validations, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_githubpr_validations_total",
		Help: "Validations of the files of a pull request, by format and result.",
	}, []string{labelFormat, labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register the validation metric: %w", err)
	}

	pullRequests, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_githubpr_pull_requests_total",
		Help: "Pull requests the action opened or found already open, and attempts that failed, by result.",
	}, []string{labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register the pull request metric: %w", err)
	}

	return &metrics{validations: validations, pullRequests: pullRequests}, nil
}

// validated counts one validation of files in format.
func (m *metrics) validated(format, result string) {
	m.validations.WithLabelValues(format, result).Inc()
}

// pullRequest counts one attempt to open a pull request.
func (m *metrics) pullRequest(result string) {
	m.pullRequests.WithLabelValues(result).Inc()
}

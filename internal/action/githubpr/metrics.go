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
	labelState  = "state"
)

// Results of a validation.
const (
	resultPassed      = "passed"
	resultFailed      = "failed"
	resultUnavailable = "unavailable"
)

// Results of reading the state of a pull request, besides its states.
const (
	resultMissing = "missing"
)

// tracerName names the tracer of the status job.
const tracerName = "github.com/nourtalaat/sidekick/internal/action/githubpr"

// Results of opening a pull request.
const (
	resultOpened   = "opened"
	resultExisting = "existing"
	resultError    = "error"
	resultApplied  = "applied"
	resultClosed   = "closed"
	resultReverted = "reverted"
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
		Help: "Pull requests the action opened (opened), found already open (existing), closed or reverted when undone (closed, reverted), found on the base branch already (applied), and attempts that failed (error), by result.",
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

// newStatusMetric creates the counter of the status job's checks and
// registers it with reg, which may be nil.
func newStatusMetric(reg prometheus.Registerer) (*prometheus.CounterVec, error) {
	checks, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_githubpr_status_checks_total",
		Help: "Pull requests the status job read from GitHub, by state (open, merged, closed), missing for one that was not found, and error.",
	}, []string{labelState}))
	if err != nil {
		return nil, fmt.Errorf("register the status metric: %w", err)
	}

	return checks, nil
}

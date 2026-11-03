package gitlabmr

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

// Results of opening a merge request.
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
	validations   *prometheus.CounterVec
	mergeRequests *prometheus.CounterVec
	statusChecks  *prometheus.CounterVec
}

// newMetrics creates the metrics and registers them with reg, which may be
// nil. Instances of the action share one set of collectors.
func newMetrics(reg prometheus.Registerer) (*metrics, error) {
	validations, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_gitlabmr_validations_total",
		Help: "Validations of the files of a merge request, by format and result.",
	}, []string{labelFormat, labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register the validation metric: %w", err)
	}

	mergeRequests, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_gitlabmr_merge_requests_total",
		Help: "Merge requests the action opened (opened), found already open (existing), closed or reverted when undone (closed, reverted), found on the base branch already (applied), and attempts that failed (error), by result.",
	}, []string{labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register the merge request metric: %w", err)
	}

	statusChecks, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_gitlabmr_status_checks_total",
		Help: "Merge requests the status job read from GitLab, by state (open, merged, closed), missing for one that was not found, and error.",
	}, []string{labelState}))
	if err != nil {
		return nil, fmt.Errorf("register the status metric: %w", err)
	}

	return &metrics{validations: validations, mergeRequests: mergeRequests, statusChecks: statusChecks}, nil
}

// validated counts one validation of files in format.
func (m *metrics) validated(format, result string) {
	m.validations.WithLabelValues(format, result).Inc()
}

// mergeRequest counts one attempt to open a merge request.
func (m *metrics) mergeRequest(result string) {
	m.mergeRequests.WithLabelValues(result).Inc()
}

// statusCheck counts one check of the status job.
func (m *metrics) statusCheck(state string) {
	m.statusChecks.WithLabelValues(state).Inc()
}

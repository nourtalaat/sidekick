package gitlabgit

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// Labels of the metrics.
const (
	labelResult = "result"
	labelState  = "state"
)

// Results of opening a merge request, as the merge request counter labels
// them.
const (
	ResultOpened   = "opened"
	ResultExisting = "existing"
	ResultError    = "error"
	ResultApplied  = "applied"
	ResultClosed   = "closed"
	ResultReverted = "reverted"
)

// Metrics are the counters every merge request action has: the merge requests
// it opens and the checks of the status job.
type Metrics struct {
	mergeRequests *prometheus.CounterVec
	statusChecks  *prometheus.CounterVec
}

// NewMetrics creates the counters sidekick_<name>_merge_requests_total and
// sidekick_<name>_status_checks_total and registers them with reg, which may be
// nil. Instances of the action share one set of collectors.
func NewMetrics(reg prometheus.Registerer, name string) (*Metrics, error) {
	mergeRequests, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_" + name + "_merge_requests_total",
		Help: "Merge requests the action opened (opened), found already open (existing), closed or reverted when undone (closed, reverted), found on the base branch already (applied), and attempts that failed (error), by result.",
	}, []string{labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register the merge request metric: %w", err)
	}

	statusChecks, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_" + name + "_status_checks_total",
		Help: "Merge requests the status job read from GitLab, by state (open, merged, closed), missing for one that was not found, and error.",
	}, []string{labelState}))
	if err != nil {
		return nil, fmt.Errorf("register the status metric: %w", err)
	}

	return &Metrics{mergeRequests: mergeRequests, statusChecks: statusChecks}, nil
}

// MergeRequest counts one attempt to open a merge request, by result.
func (m *Metrics) MergeRequest(result string) {
	m.mergeRequests.WithLabelValues(result).Inc()
}

// StatusCheck counts one check of the status job.
func (m *Metrics) StatusCheck(state string) {
	m.statusChecks.WithLabelValues(state).Inc()
}

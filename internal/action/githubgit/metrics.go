package githubgit

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

// Results of opening a pull request, as the pull request counter labels
// them.
const (
	ResultOpened   = "opened"
	ResultExisting = "existing"
	ResultError    = "error"
	ResultApplied  = "applied"
	ResultClosed   = "closed"
	ResultReverted = "reverted"
)

// Metrics are the counters every pull request action has: the pull requests
// it opens and the checks of the status job.
type Metrics struct {
	pullRequests *prometheus.CounterVec
	statusChecks *prometheus.CounterVec
}

// NewMetrics creates the counters sidekick_<name>_pull_requests_total and
// sidekick_<name>_status_checks_total and registers them with reg, which may
// be nil. Instances of the action share one set of collectors.
func NewMetrics(reg prometheus.Registerer, name string) (*Metrics, error) {
	pullRequests, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_" + name + "_pull_requests_total",
		Help: "Pull requests the action opened (opened), found already open (existing), closed or reverted when undone (closed, reverted), found on the base branch already (applied), and attempts that failed (error), by result.",
	}, []string{labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register the pull request metric: %w", err)
	}

	statusChecks, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_" + name + "_status_checks_total",
		Help: "Pull requests the status job read from GitHub, by state (open, merged, closed), missing for one that was not found, and error.",
	}, []string{labelState}))
	if err != nil {
		return nil, fmt.Errorf("register the status metric: %w", err)
	}

	return &Metrics{pullRequests: pullRequests, statusChecks: statusChecks}, nil
}

// PullRequest counts one attempt to open a pull request, by result.
func (m *Metrics) PullRequest(result string) {
	m.pullRequests.WithLabelValues(result).Inc()
}

// StatusCheck counts one check of the status job.
func (m *Metrics) StatusCheck(state string) {
	m.statusChecks.WithLabelValues(state).Inc()
}

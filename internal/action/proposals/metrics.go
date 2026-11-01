package proposals

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Metric labels.
const (
	labelAction = "action"
	labelStatus = "status"
)

// Metrics are the proposal counters. Pass them to New in Options.Metrics.
// A nil *Metrics records nothing.
type Metrics struct {
	total *prometheus.CounterVec
}

// NewMetrics creates sidekick_actions_total, a counter by action name and
// status of the proposals that reached the status on this replica, and
// registers it with reg. Several calls on one registry share the counter.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	counter, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_actions_total",
		Help: "Action proposals by action and the status they reached.",
	}, []string{labelAction, labelStatus}))
	if err != nil {
		return nil, fmt.Errorf("register the proposal metrics: %w", err)
	}

	return &Metrics{total: counter}, nil
}

// reached counts a proposal of actionName that reached status.
func (m *Metrics) reached(actionName string, status action.ProposalStatus) {
	if m == nil {
		return
	}

	m.total.WithLabelValues(actionName, string(status)).Inc()
}

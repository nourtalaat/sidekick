package gitlabmr

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Labels of the metrics.
const (
	labelFormat = "format"
	labelResult = "result"
)

// metrics are the action's Prometheus metrics, shared by every instance.
type metrics struct {
	validations *prometheus.CounterVec
	*gitlabgit.Metrics
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

	shared, err := gitlabgit.NewMetrics(reg, "gitlabmr")
	if err != nil {
		return nil, fmt.Errorf("set up the merge request metrics: %w", err)
	}

	return &metrics{validations: validations, Metrics: shared}, nil
}

// validated counts one validation of files in format.
func (m *metrics) validated(format, result string) {
	m.validations.WithLabelValues(format, result).Inc()
}

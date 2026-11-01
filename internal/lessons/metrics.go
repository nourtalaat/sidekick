package lessons

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// MetricDrafts counts the draft_lesson jobs that ended, by result.
const MetricDrafts = "sidekick_lesson_drafts_total"

// labelResult is the label of MetricDrafts.
const labelResult = "result"

// The results of a draft_lesson job.
const (
	// resultProposed: a lesson was stored and waits for an admin.
	resultProposed = "proposed"
	// resultActive: a lesson was stored and is in use.
	resultActive = "active"
	// resultInvalid: the model gave no answer Sidekick could use in 3 calls.
	resultInvalid = "invalid"
	// resultSkipped: a lesson was drafted from the feedback already, or the
	// feedback or its investigation is gone.
	resultSkipped = "skipped"
	// resultOverCap: a daily cap was reached, so the job moved to the next
	// day.
	resultOverCap = "over_cap"
)

// Metrics are the lesson metrics. A nil *Metrics records nothing.
type Metrics struct {
	drafts *prometheus.CounterVec
}

// NewMetrics makes the lesson metrics and registers them with reg.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	drafts, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricDrafts,
		Help: "Lesson drafts that ended, by result: proposed or active for a lesson stored, invalid when the model gave no usable answer, " +
			"skipped when there was nothing to draft, and over_cap when a daily cap moved the draft to the next day.",
	}, []string{labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register lesson metrics: %w", err)
	}

	for _, r := range []string{resultProposed, resultActive, resultInvalid, resultSkipped, resultOverCap} {
		drafts.WithLabelValues(r)
	}

	return &Metrics{drafts: drafts}, nil
}

// drafted counts one draft with result.
func (m *Metrics) drafted(result string) {
	if m == nil {
		return
	}

	m.drafts.WithLabelValues(result).Inc()
}

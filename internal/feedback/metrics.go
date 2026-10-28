package feedback

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// MetricFeedback counts stored ratings.
const MetricFeedback = "sidekick_feedback_total"

// Metric labels.
const (
	labelTarget = "target"
	labelRating = "rating"
)

// Metrics are the feedback metrics. A nil *Metrics records nothing.
type Metrics struct {
	feedback *prometheus.CounterVec
}

// NewMetrics makes the feedback metrics and registers them with reg. Called
// again with the same reg, it returns metrics that count into the same
// counter, as the Slack app and the API both need.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	counter, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricFeedback,
		Help: "Ratings responders stored, by target kind and rating. A repeated click is not counted.",
	}, []string{labelTarget, labelRating}))
	if err != nil {
		return nil, fmt.Errorf("register feedback metrics: %w", err)
	}

	for _, r := range []output.Rating{output.RatingCorrect, output.RatingWrong} {
		counter.WithLabelValues(TargetVerdict, string(r))
	}

	return &Metrics{feedback: counter}, nil
}

// Stored counts one stored rating of target.
func (m *Metrics) Stored(target string, rating output.Rating) {
	if m == nil {
		return
	}

	m.feedback.WithLabelValues(target, string(rating)).Inc()
}

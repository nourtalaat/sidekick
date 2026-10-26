package safehttp

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// MetricBlocked counts the requests and redirects the rules refused.
const MetricBlocked = "sidekick_watch_http_blocked_total"

// labelReason is the label of MetricBlocked.
const labelReason = "reason"

// Metrics are the client's metrics. A nil *Metrics records nothing.
type Metrics struct {
	blocked *prometheus.CounterVec
}

// NewMetrics makes the client's metrics and registers them with reg. Every
// reason starts at zero.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{blocked: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: MetricBlocked,
		Help: "Watcher HTTP check requests the safety rules refused, by reason: host, private_ip or redirect.",
	}, []string{labelReason})}

	if err := reg.Register(m.blocked); err != nil {
		return nil, fmt.Errorf("register HTTP check metrics: %w", err)
	}

	for _, r := range []Reason{ReasonHost, ReasonPrivateIP, ReasonRedirect} {
		m.blocked.WithLabelValues(string(r))
	}

	return m, nil
}

// recordBlocked counts one refused request or redirect.
func (m *Metrics) recordBlocked(r Reason) {
	if m == nil {
		return
	}

	m.blocked.WithLabelValues(string(r)).Inc()
}

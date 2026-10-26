package webhook

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"
)

// The label names and values of the intake metrics.
const (
	labelIntake = "intake"
	labelResult = "result"
	labelReason = "reason"

	resultAccepted  = "accepted"
	resultDuplicate = "duplicate"
	resultRejected  = "rejected"

	reasonUnauthenticated = "unauthenticated"
	reasonTooLarge        = "too_large"
	reasonInvalidPayload  = "invalid_payload"

	// unknownIntake is the intake label of deliveries to an endpoint that
	// is not configured, so a sender cannot add label values. Endpoint
	// names cannot start with "_", so it never mixes with a real one.
	unknownIntake = "_unknown"
)

// Metrics are the intake metrics. A nil *Metrics records nothing.
type Metrics struct {
	received *prometheus.CounterVec
	rejected *prometheus.CounterVec
}

// NewMetrics creates the intake metrics and registers them with reg:
//
//   - sidekick_events_received_total, by intake and result: accepted and
//     duplicate count events, rejected counts deliveries that were turned
//     away before any event was stored;
//   - sidekick_events_rejected_total, by intake and reason
//     (unauthenticated, too_large or invalid_payload), the same rejected
//     deliveries by why they were turned away.
//
// Deliveries that fail because of Sidekick itself, such as a secret that
// cannot be read or a database error, are logged, not counted.
func NewMetrics(reg prometheus.Registerer) (*Metrics, error) {
	m := &Metrics{
		received: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sidekick_events_received_total",
			Help: "Webhook events by intake and result: accepted and duplicate events, and rejected deliveries.",
		}, []string{labelIntake, labelResult}),
		rejected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "sidekick_events_rejected_total",
			Help: "Rejected webhook deliveries by intake and reason (unauthenticated, too_large or invalid_payload).",
		}, []string{labelIntake, labelReason}),
	}

	for _, c := range []prometheus.Collector{m.received, m.rejected} {
		if err := reg.Register(c); err != nil {
			return nil, fmt.Errorf("register intake metrics: %w", err)
		}
	}

	return m, nil
}

// init sets every series of the endpoint to zero, so rates work from the
// first delivery on.
func (m *Metrics) init(intake string) {
	if m == nil {
		return
	}

	for _, result := range []string{resultAccepted, resultDuplicate, resultRejected} {
		m.received.WithLabelValues(intake, result)
	}

	for _, reason := range []string{reasonUnauthenticated, reasonTooLarge, reasonInvalidPayload} {
		m.rejected.WithLabelValues(intake, reason)
	}
}

// reject counts a delivery turned away for reason.
func (m *Metrics) reject(intake, reason string) {
	if m == nil {
		return
	}

	m.received.WithLabelValues(intake, resultRejected).Inc()
	m.rejected.WithLabelValues(intake, reason).Inc()
}

// stored counts the accepted and duplicate events of a delivery.
func (m *Metrics) stored(intake string, stored []storedEvent) {
	if m == nil {
		return
	}

	for _, s := range stored {
		result := resultAccepted
		if s.duplicate {
			result = resultDuplicate
		}

		m.received.WithLabelValues(intake, result).Inc()
	}
}

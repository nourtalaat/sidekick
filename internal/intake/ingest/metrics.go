package ingest

import (
	"fmt"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// The label names of the intake metrics.
const (
	labelIntake = "intake"
	labelResult = "result"
	labelReason = "reason"
)

// The result label values of sidekick_events_received_total.
const (
	ResultAccepted  = "accepted"
	ResultDuplicate = "duplicate"
	ResultRejected  = "rejected"
)

// The reason label values of sidekick_events_rejected_total.
const (
	ReasonUnauthenticated = "unauthenticated"
	ReasonTooLarge        = "too_large"
	ReasonInvalidPayload  = "invalid_payload"
)

// Metrics are the intake metrics, shared by every intake. A nil *Metrics
// records nothing.
type Metrics struct {
	received *prometheus.CounterVec
	rejected *prometheus.CounterVec
}

// NewMetrics creates the intake metrics and registers them with reg, or
// takes the ones another intake registered there:
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
	received, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_events_received_total",
		Help: "Intake events by intake and result: accepted and duplicate events, and rejected deliveries.",
	}, []string{labelIntake, labelResult}))
	if err != nil {
		return nil, fmt.Errorf("register intake metrics: %w", err)
	}

	rejected, err := registry.RegisterOrExisting(reg, prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "sidekick_events_rejected_total",
		Help: "Rejected intake deliveries by intake and reason (unauthenticated, too_large or invalid_payload).",
	}, []string{labelIntake, labelReason}))
	if err != nil {
		return nil, fmt.Errorf("register intake metrics: %w", err)
	}

	return &Metrics{received: received, rejected: rejected}, nil
}

// Init sets every series of intake to zero, so rates work from the first
// delivery on.
func (m *Metrics) Init(intake string) {
	if m == nil {
		return
	}

	for _, result := range []string{ResultAccepted, ResultDuplicate, ResultRejected} {
		m.received.WithLabelValues(intake, result)
	}

	for _, reason := range []string{ReasonUnauthenticated, ReasonTooLarge, ReasonInvalidPayload} {
		m.rejected.WithLabelValues(intake, reason)
	}
}

// Reject counts a delivery to intake turned away for reason.
func (m *Metrics) Reject(intake, reason string) {
	if m == nil {
		return
	}

	m.received.WithLabelValues(intake, ResultRejected).Inc()
	m.rejected.WithLabelValues(intake, reason).Inc()
}

// Stored counts the accepted and duplicate events of a delivery to intake.
func (m *Metrics) Stored(intake string, stored []Stored) {
	if m == nil {
		return
	}

	for _, s := range stored {
		result := ResultAccepted
		if s.Duplicate {
			result = ResultDuplicate
		}

		m.received.WithLabelValues(intake, result).Inc()
	}
}

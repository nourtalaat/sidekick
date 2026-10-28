package investigator

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/model"
)

// The values of investigations.status.
const (
	// StatusQueued is an investigation that waits for its investigate job
	// to start. The router creates every investigation in it.
	StatusQueued = "queued"
	// StatusRunning is an investigation whose run has started. It stays
	// running while its job is retried or resumed on another worker.
	StatusRunning = "running"
	// StatusFinal is an investigation that ended with a report, whatever
	// its verdict and stop reason.
	StatusFinal = "final"
	// StatusFailed is an investigation that could not finish: the model
	// refused the request, or its job became dead. Its report is built from
	// the saved steps, with stop reason error.
	StatusFailed = "failed"
	// StatusCancelled is an investigation a responder stopped. Its report
	// is built from the saved steps, with stop reason cancelled.
	StatusCancelled = "cancelled"
)

// uniqueKeyPrefix starts the unique key of an investigate job.
const uniqueKeyPrefix = "investigate:"

// The values of investigations.run_kind: what the current run does. The
// run number only counts runs.
const (
	// RunKindInvestigate is a full investigation, run by an investigate
	// job.
	RunKindInvestigate = "investigate"
	// RunKindFollowUp is a follow-up run that answers questions, run by a
	// follow_up job.
	RunKindFollowUp = "follow_up"
)

// followUpKeyPrefix starts the unique key of a follow_up job.
const followUpKeyPrefix = "follow_up:"

// FollowUpUniqueKey returns the unique key of the follow_up job of the
// investigation with id, so at most one is pending or running per
// investigation, and questions that arrive while it waits share its run.
func FollowUpUniqueKey(id uuid.UUID) string {
	return followUpKeyPrefix + id.String()
}

// Payload is the payload of an investigate job, and of a follow_up job.
type Payload struct {
	InvestigationID uuid.UUID `json:"investigation_id"`
	GroupID         uuid.UUID `json:"group_id"`
}

// UniqueKey returns the unique key of the investigate job of the group with
// groupID, so at most one is pending or running per group.
func UniqueKey(groupID uuid.UUID) string {
	return uniqueKeyPrefix + groupID.String()
}

// Connectors is the content of investigations.connectors: the connector,
// and the model it was configured with, that fills each role.
type Connectors map[model.Role]RoleConnector

// RoleConnector is the connector that fills one role.
type RoleConnector struct {
	// Connector is the name under model.connectors.
	Connector string `json:"connector"`
	// Model is the model ID the connector was configured with. It is empty
	// for a connector whose config does not use the shared model settings.
	Model string `json:"model,omitempty"`
}

// Budget is the content of investigations.budget: the limits in effect for
// one investigation, fixed when it is created. Zero means no limit, which
// is also what a budget stored before a limit existed decodes to.
type Budget struct {
	// MaxTokens counts input tokens, including those read from or written
	// to the prompt cache, and output tokens.
	MaxTokens     int64
	MaxToolCalls  int
	MaxWallTime   time.Duration
	MaxCostMicros int64
}

// budgetJSON is how a Budget is stored. The wall time is in milliseconds,
// so the stored value does not depend on Go's duration format.
type budgetJSON struct {
	MaxTokens     int64 `json:"max_tokens,omitempty"`
	MaxToolCalls  int   `json:"max_tool_calls"`
	MaxWallTimeMS int64 `json:"max_wall_time_ms"`
	MaxCostMicros int64 `json:"max_cost_micros,omitempty"`
}

// MarshalJSON stores the wall time in milliseconds.
func (b Budget) MarshalJSON() ([]byte, error) {
	out, err := json.Marshal(budgetJSON{
		MaxTokens:     b.MaxTokens,
		MaxToolCalls:  b.MaxToolCalls,
		MaxWallTimeMS: b.MaxWallTime.Milliseconds(),
		MaxCostMicros: b.MaxCostMicros,
	})
	if err != nil {
		return nil, fmt.Errorf("encode the budget: %w", err)
	}

	return out, nil
}

// UnmarshalJSON reads a budget stored by MarshalJSON.
func (b *Budget) UnmarshalJSON(data []byte) error {
	var in budgetJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return fmt.Errorf("decode the budget: %w", err)
	}

	*b = Budget{
		MaxTokens:     in.MaxTokens,
		MaxToolCalls:  in.MaxToolCalls,
		MaxWallTime:   time.Duration(in.MaxWallTimeMS) * time.Millisecond,
		MaxCostMicros: in.MaxCostMicros,
	}

	return nil
}

// FollowUp returns the budget of each follow-up run of an investigation
// with budget b: half of each limit, and at least 1 of a limit that is
// set.
func (b Budget) FollowUp() Budget {
	return Budget{
		MaxTokens:     followUpShare(b.MaxTokens),
		MaxToolCalls:  followUpShare(b.MaxToolCalls),
		MaxWallTime:   followUpShare(b.MaxWallTime),
		MaxCostMicros: followUpShare(b.MaxCostMicros),
	}
}

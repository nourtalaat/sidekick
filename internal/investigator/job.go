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
)

// uniqueKeyPrefix starts the unique key of an investigate job.
const uniqueKeyPrefix = "investigate:"

// Payload is the payload of an investigate job.
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
// one investigation, fixed when it is created. Zero means no limit.
type Budget struct {
	MaxToolCalls int
	MaxWallTime  time.Duration
}

// budgetJSON is how a Budget is stored. The wall time is in milliseconds,
// so the stored value does not depend on Go's duration format.
type budgetJSON struct {
	MaxToolCalls  int   `json:"max_tool_calls"`
	MaxWallTimeMS int64 `json:"max_wall_time_ms"`
}

// MarshalJSON stores the wall time in milliseconds.
func (b Budget) MarshalJSON() ([]byte, error) {
	out, err := json.Marshal(budgetJSON{MaxToolCalls: b.MaxToolCalls, MaxWallTimeMS: b.MaxWallTime.Milliseconds()})
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

	*b = Budget{MaxToolCalls: in.MaxToolCalls, MaxWallTime: time.Duration(in.MaxWallTimeMS) * time.Millisecond}

	return nil
}

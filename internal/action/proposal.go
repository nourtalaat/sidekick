package action

import (
	"encoding/json"
	"time"
)

// ProposalStatus tracks a proposal from creation to result.
type ProposalStatus string

// Proposal statuses.
const (
	StatusProposed  ProposalStatus = "proposed"
	StatusApproved  ProposalStatus = "approved"
	StatusRejected  ProposalStatus = "rejected"
	StatusExpired   ProposalStatus = "expired"
	StatusExecuting ProposalStatus = "executing"
	StatusSucceeded ProposalStatus = "succeeded"
	StatusFailed    ProposalStatus = "failed"
	StatusUndone    ProposalStatus = "undone"
)

// Proposal is an action the model asked for, as shown to approvers.
type Proposal struct {
	ID              string
	InvestigationID string
	Action          string
	Params          json.RawMessage
	Reason          string
	Status          ProposalStatus
	ExpiresAt       time.Time
	DecidedBy       string
	// Snapshot is set for actions that implement Snapshotter.
	Snapshot *Snapshot
	// UndoOf is set on a proposal that restores an earlier one.
	UndoOf string
	Result *Result
}

package action

import (
	"encoding/json"
	"time"
)

// Defaults of the actions section of the config.
const (
	// DefaultProposalTTL is how long a proposal waits for a decision.
	DefaultProposalTTL = 24 * time.Hour
	// DefaultMaxPerInvestigation is the most proposals one investigation
	// may make.
	DefaultMaxPerInvestigation = 5
)

// idempotencyKeyPrefix starts the idempotency key of every proposal.
const idempotencyKeyPrefix = "sidekick/"

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

// Final reports whether no change of status can follow s.
func (s ProposalStatus) Final() bool {
	switch s {
	case StatusRejected, StatusExpired, StatusSucceeded, StatusFailed, StatusUndone:
		return true
	default:
		return false
	}
}

// Proposal is an action the model asked for, as shown to approvers.
//
// Params, ParamsText and Reason are text the model wrote, so every place
// that shows them treats them as untrusted. Params never change after the
// proposal is stored, so what an approver saw is what runs.
type Proposal struct {
	ID              string
	InvestigationID string
	// Route is the route of the investigation.
	Route  string
	Action string
	// Params is the stored form of the parameters, which is also what runs.
	Params json.RawMessage
	// ParamsText is DisplayParams of Params: the text approvers read.
	ParamsText string
	Reason     string
	// Mode is the investigation's action mode when it was proposed.
	Mode       Mode
	Status     ProposalStatus
	ProposedAt time.Time
	ExpiresAt  time.Time
	// DecidedBy is the ID of the Sidekick user or API token that approved
	// or rejected it, DecidedByName their name, and DecidedBySlackUser the
	// Slack user ID of someone who decided in Slack. A decision in Slack
	// may have no Sidekick user. All are empty until it is decided.
	DecidedBy          string
	DecidedByName      string
	DecidedBySlackUser string
	// ApprovedByPolicy is true when nobody decided it: the investigation
	// ran in read-write mode, so the route's policy approved it when it
	// was stored, and DecidedAt is the time of that approval.
	ApprovedByPolicy bool
	// DecidedAt is zero until it is decided.
	DecidedAt time.Time
	// Snapshot is set for actions that implement Snapshotter.
	Snapshot *Snapshot
	// Changes is the field-by-field difference of Snapshot, for people to
	// read. Empty without a snapshot.
	Changes []Change
	// Revisions is set when Snapshot is the snapshot of a rollback: the
	// revision running now and the one it goes back to, in the form
	// Revisions.Display writes. The view shows it in place of Changes,
	// which is still computed.
	Revisions *Revisions
	// Undoable says an approver may undo it: it succeeded, the action can
	// undo it, and it is not an undo itself.
	Undoable bool
	// FixKind says how the proposal fixes a noisy monitor, and is empty for
	// a proposal that fixes none. An undo is empty too.
	FixKind FixKind
	// UndoOf is set on a proposal that restores an earlier one.
	UndoOf string
	Result *Result
	// Error says why it failed. While it is retried it holds the last
	// failed attempt.
	Error string
}

// PolicyApprovalText is how a screen says that a route's policy approved a
// proposal, in place of the name of a person.
const PolicyApprovalText = "approved by policy (read + write route)"

// IdempotencyKey returns the key that Execute gets as
// Invocation.IdempotencyKey for the proposal with proposalID. It is the
// same on every attempt, for example "sidekick/<id>", which an action can
// use as a branch name or a request header.
func IdempotencyKey(proposalID string) string {
	return idempotencyKeyPrefix + proposalID
}

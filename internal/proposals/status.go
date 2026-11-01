package proposals

import "github.com/google/uuid"

// The statuses of a proposal, as strategy_proposals.status stores them.
const (
	// StatusDrafting: the proposal waits for the summarize model's draft.
	StatusDrafting = "drafting"
	// StatusDraftFailed: no draft could be made. The reason is in the
	// status detail. A strategy version gets one proposal, so the version
	// gets no other.
	StatusDraftFailed = "draft_failed"
	// StatusPending: drafted, and waiting for the gate to start.
	StatusPending = "pending"
	// StatusBenchmarking: the gate is running.
	StatusBenchmarking = "benchmarking"
	// StatusPassed: the gate passed, and an admin can approve the proposal.
	StatusPassed = "passed"
	// StatusFailed: the gate ended without a pass. The gate result says how.
	StatusFailed = "failed"
	// StatusApproved: an admin approved a proposal for a file strategy,
	// which Sidekick cannot change. The admin takes the YAML.
	StatusApproved = "approved"
	// StatusApplied: an admin approved the proposal and it became a version
	// of a runtime strategy.
	StatusApplied = "applied"
	// StatusRejected: an admin rejected the proposal.
	StatusRejected = "rejected"
)

// The audit log names of changes to proposals.
const (
	ActionCreated  = "strategy_proposal.created"
	ActionDrafted  = "strategy_proposal.drafted"
	ActionGate     = "strategy_proposal.gate_ended"
	ActionEdited   = "strategy_proposal.edited"
	ActionApproved = "strategy_proposal.approved"
	ActionRejected = "strategy_proposal.rejected"
	// KindProposal is the kind of what those entries change.
	KindProposal = "strategy_proposal"
)

// ProposeStrategyPayload is the payload of a propose_strategy job.
type ProposeStrategyPayload struct {
	ProposalID uuid.UUID `json:"proposalId"`
}

// BenchGatePayload is the payload of a bench_gate job.
type BenchGatePayload struct {
	ProposalID uuid.UUID `json:"proposalId"`
}

// ProposeStrategyKey is the unique key of the propose_strategy job of a
// proposal, so at most one is pending or running.
func ProposeStrategyKey(id uuid.UUID) string {
	return "propose_strategy:" + id.String()
}

// BenchGateKey is the unique key of the bench_gate job of a proposal.
func BenchGateKey(id uuid.UUID) string {
	return "bench_gate:" + id.String()
}

//go:build integration

package proposals

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
)

// gatedInput is a valid propose_action call for the fake approval-only action.
const gatedInput = `{"action": "rollback", "params": {"board": "ops", "title": "Roll back"}, "reason": "The deploy broke checkout."}`

// withGated adds the fake approval-only action to the harness's service and
// makes the payments route read-write with it, for a read-write investigation.
func (h *harness) withGated(t *testing.T, allowReadWrite bool) *fakeGatedNote {
	t.Helper()

	gated := newFakeGatedNote(gatedName, allowReadWrite)

	opts := Options{Pool: h.pool, Policy: h.policy, Now: h.clock.Now, Actions: map[string]action.Action{"monitor-note": h.note, gatedName: gated}}

	svc, err := New(opts)
	require.NoError(t, err)

	h.svc = svc
	h.setInvestigationMode(t, "read-write")
	h.policy.set("payments", action.RoutePolicy{Mode: action.ReadWrite, Actions: []string{"monitor-note", gatedName}})

	return gated
}

func (h *harness) proposeGated(t *testing.T) ProposeResult {
	t.Helper()

	res, err := h.svc.Propose(t.Context(), ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: "call-gated", Route: "payments", Mode: action.ReadWrite, Input: json.RawMessage(gatedInput),
	})
	require.NoError(t, err)
	require.Empty(t, res.Refusal)

	return res
}

func TestReadWriteProposalOfAnApprovalOnlyActionWaitsForAPerson(t *testing.T) {
	t.Parallel()

	h := setup(t)
	gated := h.withGated(t, false)

	res := h.proposeGated(t)
	require.False(t, res.ApprovedByPolicy)
	require.True(t, res.HeldForPerson)

	row := h.row(t, res.ID)
	require.Equal(t, "proposed", row.Status)
	require.Equal(t, "approved", row.Mode, "the policy did not apply")
	require.Nil(t, row.DecidedAt)
	require.Equal(t, []string{AuditProposed}, h.auditActions(t, res.ID))
	require.Empty(t, h.jobs(t))

	again := h.proposeGated(t)
	require.Equal(t, res.ID, again.ID)
	require.True(t, again.HeldForPerson)

	_, err := h.svc.Approve(t.Context(), res.ID, Decider{UserID: anaUser})
	require.NoError(t, err, "a person can approve it")

	ran, err := h.runJob(t)
	require.NoError(t, err)
	require.True(t, ran)
	require.Equal(t, "succeeded", h.row(t, res.ID).Status)
	require.Len(t, gated.callList(), 1)
	require.Equal(t, anaUser.String(), gated.callList()[0].ApprovedBy)
}

func TestReadWriteProposalOfAnApprovalOnlyActionThatOptedInIsApprovedByPolicy(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.withGated(t, true)

	res := h.proposeGated(t)
	require.True(t, res.ApprovedByPolicy)
	require.False(t, res.HeldForPerson)

	row := h.row(t, res.ID)
	require.Equal(t, "approved", row.Status)
	require.Equal(t, "read-write", row.Mode)
	require.Equal(t, []string{AuditProposed, AuditApproved}, h.auditActions(t, res.ID))
	require.Len(t, h.jobs(t), 1)
}

func TestApprovedByPolicyProposalFailsWhenTheActionIsBlockedBeforeItRuns(t *testing.T) {
	t.Parallel()

	h := setup(t)
	gated := h.withGated(t, true)

	res := h.proposeGated(t)
	require.True(t, res.ApprovedByPolicy)

	gated.allowReadWrite(false)

	ran, err := h.runJob(t)
	require.NoError(t, err, "the job is done")
	require.True(t, ran)

	row := h.row(t, res.ID)
	require.Equal(t, "failed", row.Status)
	require.NotNil(t, row.Error)
	require.Contains(t, *row.Error, `action "rollback" is approval-only and does not allow read-write mode`)
	require.Empty(t, gated.callList(), "nothing ran")
	require.Equal(t, 0, gated.effectCount())
	require.Equal(t, AuditFailed, last(h.auditActions(t, res.ID)))
	require.Contains(t, h.auditActors(t, res.ID), audit.ActorPolicy)
}

func TestPersonApprovedProposalRunsWhenTheActionIsBlocked(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.withGated(t, false)

	res := h.proposeGated(t)

	_, err := h.svc.Approve(t.Context(), res.ID, Decider{UserID: anaUser})
	require.NoError(t, err)

	ran, err := h.runJob(t)
	require.NoError(t, err)
	require.True(t, ran)
	require.Equal(t, "succeeded", h.row(t, res.ID).Status, "a person approved it, so the gate does not apply")
}

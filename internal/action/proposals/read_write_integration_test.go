//go:build integration

package proposals

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/audit"
)

// readWrite turns the checkout investigation and its route to read-write.
func (h *harness) readWrite(t *testing.T) {
	t.Helper()

	h.setInvestigationMode(t, "read-write")
	h.policy.set("payments", action.RoutePolicy{Mode: action.ReadWrite, Actions: []string{"monitor-note"}})
}

func (h *harness) setInvestigationMode(t *testing.T, mode string) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE investigations SET mode = $2 WHERE id = $1", checkoutInvestigation, mode)
	require.NoError(t, err)
}

// proposeAs makes the note call on the checkout investigation in mode.
func (h *harness) proposeAs(t *testing.T, mode action.Mode) ProposeResult {
	t.Helper()

	res, err := h.svc.Propose(t.Context(), ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: "call-rw", Route: "payments", Mode: mode, Input: json.RawMessage(noteInput),
	})
	require.NoError(t, err)
	require.Empty(t, res.Refusal)

	return res
}

// auditActors returns the actor kind of each audit entry of the proposal.
func (h *harness) auditActors(t *testing.T, id uuid.UUID) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT actor_kind FROM audit_log WHERE target_kind = 'action_proposal' AND target_id = $1 ORDER BY id", id.String())
	require.NoError(t, err)

	kinds, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return kinds
}

func TestProposeInReadWriteModeIsApprovedByPolicy(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.readWrite(t)
	before := h.publishSeq(t, checkoutInvestigation)

	res := h.proposeAs(t, action.ReadWrite)
	require.True(t, res.ApprovedByPolicy)

	row := h.row(t, res.ID)
	require.Equal(t, "approved", row.Status)
	require.Equal(t, "read-write", row.Mode)
	require.Nil(t, row.DecidedBy)
	require.Nil(t, row.DecidedBySlackUser)
	require.NotNil(t, row.DecidedAt, "the dashboard counts a proposal as approved when decided_at is set")
	require.True(t, h.clock.Now().Equal(*row.DecidedAt))

	require.Equal(t, []string{AuditProposed, AuditApproved}, h.auditActions(t, res.ID))
	require.Equal(t, []string{audit.ActorSidekick, audit.ActorPolicy}, h.auditActors(t, res.ID))
	require.Greater(t, h.publishSeq(t, checkoutInvestigation), before)

	jobs := h.jobs(t)
	require.Len(t, jobs, 1)
	require.Equal(t, UniqueKey(res.ID), *jobs[0].UniqueKey)

	proposals, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
	require.NoError(t, err)
	require.Len(t, proposals, 1)
	require.True(t, proposals[0].ApprovedByPolicy)

	ran, err := h.runJob(t)
	require.NoError(t, err)
	require.True(t, ran)

	require.Equal(t, "succeeded", h.row(t, res.ID).Status)
	require.Equal(t, 1, h.note.effectCount())

	calls := h.note.callList()
	require.Len(t, calls, 1)
	require.Equal(t, audit.ActorPolicy, calls[0].ApprovedBy)

	got, err := Get(t.Context(), h.pool, res.ID)
	require.NoError(t, err)
	require.True(t, got.ApprovedByPolicy)
	require.Equal(t, action.StatusSucceeded, got.Status)
}

func TestProposeInReadWriteModeRepeatedAfterACrash(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.readWrite(t)

	first := h.proposeAs(t, action.ReadWrite)
	second := h.proposeAs(t, action.ReadWrite)

	require.Equal(t, first.ID, second.ID)
	require.True(t, second.ApprovedByPolicy)
	require.Len(t, h.jobs(t), 1)
	require.Equal(t, []string{AuditProposed, AuditApproved}, h.auditActions(t, first.ID))
}

func TestProposeWaitsForAPersonUnlessTheModeAndTheRouteAreReadWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// setup changes the harness before the call.
		setup func(t *testing.T, h *harness)
		mode  action.Mode
		// wantMode is the mode the proposal is stored with.
		wantMode string
	}{
		{
			name: "an approved investigation", mode: action.Approved, wantMode: "approved",
			setup: func(t *testing.T, h *harness) { t.Helper() },
		},
		{
			name: "a route that is approved now", mode: action.ReadWrite, wantMode: "read-write",
			setup: func(t *testing.T, h *harness) {
				t.Helper()
				h.setInvestigationMode(t, "read-write")
			},
		},
		{
			name: "an investigation that an event that is not trusted joined", mode: action.ReadWrite, wantMode: "approved",
			setup: func(t *testing.T, h *harness) {
				t.Helper()
				h.policy.set("payments", action.RoutePolicy{Mode: action.ReadWrite, Actions: []string{"monitor-note"}})
				h.setInvestigationMode(t, "approved")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			tt.setup(t, h)

			res := h.proposeAs(t, tt.mode)
			require.False(t, res.ApprovedByPolicy)

			row := h.row(t, res.ID)
			require.Equal(t, "proposed", row.Status)
			require.Equal(t, tt.wantMode, row.Mode)
			require.Nil(t, row.DecidedAt)
			require.Equal(t, []string{AuditProposed}, h.auditActions(t, res.ID))
			require.Empty(t, h.jobs(t))

			_, err := h.svc.Approve(t.Context(), res.ID, Decider{UserID: anaUser})
			if tt.wantMode == "read-write" {
				require.NoError(t, err, "a person can still approve it")
			}
		})
	}
}

//go:build integration

package proposals

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// rollbackName is the name of the fake rollback action in the config.
const rollbackName = "rollback"

// setupRollback is a harness whose route payments also lists the fake
// rollback action, which may go back as far as maxBack, or one revision when
// maxBack is nil.
func setupRollback(t *testing.T, maxBack *int) (*harness, *fakeRollback) {
	t.Helper()

	h := setup(t)
	rb := newFakeRollback(rollbackName, maxBack)

	h.policy.set("payments", action.RoutePolicy{Mode: action.Approved, Actions: []string{"monitor-note", "monitor", rollbackName}})

	svc, err := New(Options{
		Pool: h.pool, Policy: h.policy, Now: h.clock.Now,
		Actions: map[string]action.Action{"monitor-note": h.note, "monitor": h.monitor, rollbackName: rb},
	})
	require.NoError(t, err)

	h.svc = svc

	return h, rb
}

// rollbackInput is a propose_action call that goes back this many revisions.
func rollbackInput(back int) string {
	params, _ := json.Marshal(map[string]int{"back": back})

	return `{"action": "` + rollbackName + `", "params": ` + string(params) + `, "reason": "The errors began with revision 7."}`
}

// ranRollback proposes a rollback of back revisions, approves it as Ana and
// runs it, and returns its ID.
func (h *harness) ranRollback(t *testing.T, back int) uuid.UUID {
	t.Helper()

	id := h.propose(t, "call-1", rollbackInput(back))
	_, err := h.svc.Approve(t.Context(), id, Decider{UserID: anaUser})
	require.NoError(t, err)

	ran, err := h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err)
	require.Equal(t, "succeeded", h.row(t, id).Status)

	return id
}

func TestARollbackProposalCarriesBothRevisions(t *testing.T) {
	t.Parallel()

	three := 3
	h, _ := setupRollback(t, &three)

	id := h.propose(t, "call-1", rollbackInput(2))

	view, err := Get(t.Context(), h.pool, id)
	require.NoError(t, err)
	require.NotNil(t, view.Revisions)

	running, goesBackTo := view.Revisions.Running, view.Revisions.GoesBackTo
	require.Equal(t, fakeRevisionKind, running.Kind)
	require.Equal(t, "7", running.ID)
	require.Equal(t, "checkout:1.4.7", running.Version)
	require.Equal(t, "ana", running.DeployedBy)
	require.NotNil(t, running.DeployedAt)

	require.Equal(t, "5", goesBackTo.ID)
	require.Equal(t, "checkout:1.4.5\\u200b", goesBackTo.Version, "a hidden character is spelled out")

	require.NotEmpty(t, view.Changes, "the field-by-field difference is still computed for older clients")

	listed, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
	require.NoError(t, err)
	require.Len(t, listed, 1)
	require.Equal(t, view.Revisions, listed[0].Revisions)
}

func TestARollbackBeyondTheLimitIsRefused(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		maxBack *int
		back    int
		refused bool
	}{
		{name: "one back with the default", back: 1},
		{name: "two back with the default", back: 2, refused: true},
		{name: "two back with a limit of two", maxBack: new(2), back: 2},
		{name: "three back with a limit of two", maxBack: new(2), back: 3, refused: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h, _ := setupRollback(t, tt.maxBack)

			res, err := h.svc.Propose(t.Context(), ProposeRequest{
				InvestigationID: checkoutInvestigation, ToolCallID: "call-1", Route: "payments", Mode: action.Approved, Input: json.RawMessage(rollbackInput(tt.back)),
			})
			require.NoError(t, err)

			if tt.refused {
				require.Contains(t, res.Refusal, "outside configured limits")
				require.Equal(t, uuid.Nil, res.ID)

				return
			}

			require.Empty(t, res.Refusal)
		})
	}
}

func TestTheUndoOfARollbackShowsTheRevisionsTheOtherWayRound(t *testing.T) {
	t.Parallel()

	h, rb := setupRollback(t, new(3))
	id := h.ranRollback(t, 2)
	require.Equal(t, 5, rb.runningRevision())

	undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
	require.NoError(t, err)
	require.Equal(t, action.StatusSucceeded, undo.Status)
	require.Equal(t, 7, rb.runningRevision(), "the undo put back the revision that ran before")

	require.NotNil(t, undo.Revisions)
	require.Equal(t, "5", undo.Revisions.Running.ID)
	require.Equal(t, "7", undo.Revisions.GoesBackTo.ID)

	original, err := Get(t.Context(), h.pool, id)
	require.NoError(t, err)
	require.Equal(t, "7", original.Revisions.Running.ID, "the proposal that was undone keeps its own view")
	require.Equal(t, "5", original.Revisions.GoesBackTo.ID)
}

func TestAProposalThatIsNotARollbackHasNoRevisions(t *testing.T) {
	t.Parallel()

	h, _ := setupRollback(t, nil)
	id := h.propose(t, "call-1", monitorInput)

	view, err := Get(t.Context(), h.pool, id)
	require.NoError(t, err)
	require.Nil(t, view.Revisions)
	require.NotEmpty(t, view.Changes)
}

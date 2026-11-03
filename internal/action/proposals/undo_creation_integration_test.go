//go:build integration

package proposals

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// muteInput is a valid propose_action call for the fake mute, which makes a
// mute that did not exist.
const muteInput = `{"action": "mute", "params": {"service": "checkout"}, "reason": "Planned work."}`

// muted is the live mute after the fake action made it.
const muted = `{"service":"checkout","state":"active"}`

// editedMute is the live mute after someone else changed it.
const editedMute = `{"service":"checkout","state":"edited"}`

// mutedService proposes the fake mute, approves it as Ana and runs it, and
// returns its ID.
func (h *harness) mutedService(t *testing.T) uuid.UUID {
	t.Helper()

	id := h.propose(t, "call-mute", muteInput)
	_, err := h.svc.Approve(t.Context(), id, Decider{UserID: anaUser})
	require.NoError(t, err)

	ran, err := h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err)
	require.Equal(t, "succeeded", h.row(t, id).Status)

	return id
}

func TestUndoOfWhatTheActionCreatedRemovesIt(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.mutedService(t)
	require.JSONEq(t, muted, h.mute.definition())

	proposals, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
	require.NoError(t, err)
	require.True(t, proposals[0].Undoable, "a creation can be undone")
	require.Equal(t, []action.Change{{Path: "", After: new(muted)}}, proposals[0].Changes)

	undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
	require.NoError(t, err)
	require.Equal(t, action.StatusSucceeded, undo.Status)
	require.Equal(t, "Ended the mute.", undo.Result.Summary)
	require.Empty(t, h.mute.definition())
	require.Equal(t, "undone", h.row(t, id).Status)

	require.Equal(t, []action.Change{{Path: "", Before: new(muted)}}, undo.Changes, "the undo shows the mute going away")
	require.False(t, undo.Undoable, "an undo is not undone in turn")
}

func TestUndoShowsWhatWouldBeRemovedWhenSomeoneEditedWhatTheActionCreated(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.mutedService(t)
	h.mute.edit(editedMute)

	_, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)

	conflict, ok := errors.AsType[*UndoConflictError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, []action.Change{{Path: "", Before: new(editedMute)}}, conflict.Changes,
		"the approver sees the whole live definition that a forced undo removes, and not an empty list")

	require.JSONEq(t, editedMute, h.mute.definition(), "nothing was ended")
	require.Equal(t, "succeeded", h.row(t, id).Status)

	undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, true)
	require.NoError(t, err)
	require.Equal(t, action.StatusSucceeded, undo.Status)
	require.Empty(t, h.mute.definition())
	require.Equal(t, "undone", h.row(t, id).Status)
}

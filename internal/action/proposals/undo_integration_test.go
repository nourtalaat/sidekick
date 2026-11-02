//go:build integration

package proposals

import (
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/queue"
)

// raised is the monitor after the fake action raised its threshold to 90.
const raised = `{"message":"Checkout is slow","options":{"critical":90,"notify_no_data":false},"tags":["team:payments"]}`

// editedByAPerson is the monitor after the action's change, when someone
// else then set the threshold to 95.
const editedByAPerson = `{"message":"Checkout is slow","options":{"critical":95,"notify_no_data":false},"tags":["team:payments"]}`

// changedMonitor proposes the fake monitor change, approves it as Ana and
// runs it, and returns its ID.
func (h *harness) changedMonitor(t *testing.T, callID string) uuid.UUID {
	t.Helper()

	id := h.propose(t, callID, monitorInput)
	_, err := h.svc.Approve(t.Context(), id, Decider{UserID: anaUser})
	require.NoError(t, err)

	ran, err := h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err)
	require.Equal(t, "succeeded", h.row(t, id).Status)

	return id
}

func TestUndo(t *testing.T) {
	t.Parallel()

	h := setup(t)
	h.svc = h.service(t, Options{MaxPerInvestigation: 2})

	id := h.changedMonitor(t, "call-1")
	require.JSONEq(t, raised, h.monitor.definition())

	proposals, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
	require.NoError(t, err)
	require.True(t, proposals[0].Undoable)

	undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
	require.NoError(t, err)
	require.Equal(t, action.StatusSucceeded, undo.Status)
	require.Equal(t, id.String(), undo.UndoOf)
	require.Equal(t, "Restored the monitor.", undo.Result.Summary)
	require.Equal(t, anaUser.String(), undo.DecidedBy)
	require.False(t, undo.Undoable, "an undo is not undone in turn")
	require.Equal(t, []action.Change{{Path: "options.critical", Before: new("90"), After: new("80")}}, undo.Changes)
	require.JSONEq(t, startMonitor, h.monitor.definition())

	undoID := uuid.MustParse(undo.ID)
	row := h.row(t, undoID)
	require.Equal(t, "approved", row.Mode)
	require.JSONEq(t, fmt.Sprintf(`{"force":false,"undoOf":%q}`, id), string(row.Params))
	require.Equal(t, "sidekick/"+undoID.String(), row.IdempotencyKey)
	require.False(t, row.Undoable)
	require.False(t, approvedByPolicy(row), "an undo is approved by the person who asked, never by the route's policy")
	require.False(t, undo.ApprovedByPolicy)

	require.Equal(t, "undone", h.row(t, id).Status)
	require.Equal(t, []string{AuditProposed, AuditApproved, AuditExecuting, AuditSucceeded, AuditUndone}, h.auditActions(t, id))
	require.Equal(t, []string{AuditUndoRequested, AuditSucceeded}, h.auditActions(t, undoID))

	var actor, actorID string
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT actor_kind, actor_id FROM audit_log WHERE action = $1", AuditUndoRequested).Scan(&actor, &actorID))
	require.Equal(t, "user", actor)
	require.Equal(t, anaUser.String(), actorID)

	ran, _ := h.runJob(t)
	require.False(t, ran, "the backup job waits, so it does not restore while the request does")

	h.moveBackupJobsToNow(t)

	ran, err = h.runJob(t)
	require.True(t, ran, "the backup job is claimed once its time has come")
	require.NoError(t, err)
	require.Equal(t, 1, h.monitor.undoCalls, "and finds the undo done")
	require.Equal(t, 2, h.monitor.writeCount())

	again, err := h.svc.Propose(t.Context(), ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: "call-2", Route: "payments", Mode: action.Approved, Input: []byte(monitorInput),
	})
	require.NoError(t, err)
	require.Empty(t, again.Refusal, "an undo does not count toward the cap on what the model proposes")
}

func TestUndoBySlackApprover(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.changedMonitor(t, "call-1")

	undo, err := h.svc.Undo(t.Context(), id, Decider{SlackUserID: "U0APPROVER"}, false)
	require.NoError(t, err)
	require.Equal(t, "U0APPROVER", undo.DecidedBySlackUser)
	require.Empty(t, undo.DecidedBy)

	var actorID string
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT actor_id FROM audit_log WHERE action = $1", AuditUndone).Scan(&actorID))
	require.Equal(t, "U0APPROVER", actorID)
}

func TestUndoShowsWhatSomeoneElseChanged(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.changedMonitor(t, "call-1")
	h.monitor.edit(editedByAPerson)

	_, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)

	conflict, ok := errors.AsType[*UndoConflictError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, []action.Change{{Path: "options.critical", Before: new("95"), After: new("80")}}, conflict.Changes)

	require.JSONEq(t, editedByAPerson, h.monitor.definition(), "nothing was restored")
	require.Equal(t, "succeeded", h.row(t, id).Status)

	attempts := h.undoAttempts(t, id)
	require.Len(t, attempts, 1)
	require.Equal(t, "failed", attempts[0].Status)
	require.Equal(t, errUndoConflict, *attempts[0].Error)
	require.Equal(t, []string{AuditUndoRequested, AuditFailed}, h.auditActions(t, attempts[0].ID))

	undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, true)
	require.NoError(t, err)
	require.Equal(t, action.StatusSucceeded, undo.Status)
	require.JSONEq(t, fmt.Sprintf(`{"force":true,"undoOf":%q}`, id), string(h.row(t, uuid.MustParse(undo.ID)).Params))
	require.JSONEq(t, startMonitor, h.monitor.definition())
	require.Equal(t, "undone", h.row(t, id).Status)
	require.Len(t, h.undoAttempts(t, id), 2)
}

func TestUndoKeepsEditsToOtherFields(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.changedMonitor(t, "call-1")
	h.monitor.edit(`{"message":"Checkout is slow","options":{"critical":90,"notify_no_data":false},"tags":["team:payments","team:sre"]}`)

	_, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
	require.NoError(t, err)
	require.JSONEq(t, `{"message":"Checkout is slow","options":{"critical":80,"notify_no_data":false},"tags":["team:payments","team:sre"]}`, h.monitor.definition())
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T, h *harness) uuid.UUID
		by      Decider
		wantErr error
		wantMsg string
	}{
		{
			name: "a token", by: Decider{UserID: ciToken, Token: true}, wantErr: ErrTokenCannotUndo,
			prepare: func(t *testing.T, h *harness) uuid.UUID { return h.changedMonitor(t, "call-1") },
		},
		{
			name: "nobody", by: Decider{}, wantErr: errNoDecider,
			prepare: func(t *testing.T, h *harness) uuid.UUID { return h.changedMonitor(t, "call-1") },
		},
		{
			name: "a proposal that does not exist", by: Decider{UserID: anaUser}, wantErr: ErrNotFound,
			prepare: func(t *testing.T, h *harness) uuid.UUID { return uuid.New() },
		},
		{
			name: "a proposal that waits", by: Decider{UserID: anaUser}, wantErr: ErrNotUndoable, wantMsg: "only a succeeded proposal",
			prepare: func(t *testing.T, h *harness) uuid.UUID { return h.propose(t, "call-1", monitorInput) },
		},
		{
			name: "an action that cannot undo", by: Decider{UserID: anaUser}, wantErr: ErrNotUndoable, wantMsg: "its action cannot undo it",
			prepare: func(t *testing.T, h *harness) uuid.UUID {
				id := h.approved(t)
				ran, err := h.runJob(t)
				require.True(t, ran)
				require.NoError(t, err)

				return id
			},
		},
		{
			name: "a proposal undone already", by: Decider{UserID: anaUser}, wantErr: ErrNotUndoable, wantMsg: "undone already",
			prepare: func(t *testing.T, h *harness) uuid.UUID {
				id := h.changedMonitor(t, "call-1")
				_, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
				require.NoError(t, err)

				return id
			},
		},
		{
			name: "an undo", by: Decider{UserID: anaUser}, wantErr: ErrNotUndoable, wantMsg: "an undo itself",
			prepare: func(t *testing.T, h *harness) uuid.UUID {
				id := h.changedMonitor(t, "call-1")
				undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
				require.NoError(t, err)

				return uuid.MustParse(undo.ID)
			},
		},
		{
			name: "a route that is read-only now", by: Decider{UserID: anaUser}, wantErr: ErrNotAllowed, wantMsg: "read-only",
			prepare: func(t *testing.T, h *harness) uuid.UUID {
				id := h.changedMonitor(t, "call-1")
				h.policy.set("payments", action.RoutePolicy{Mode: action.ReadOnly})

				return id
			},
		},
		{
			name: "an undo that runs already", by: Decider{UserID: anaUser}, wantErr: ErrUndoRunning,
			prepare: func(t *testing.T, h *harness) uuid.UUID {
				id := h.changedMonitor(t, "call-1")
				h.monitor.undoErrs = []error{errBackendDown}

				undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
				require.NoError(t, err)
				require.Equal(t, action.StatusExecuting, undo.Status)

				return id
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t)
			id := tt.prepare(t, h)

			_, err := h.svc.Undo(t.Context(), id, tt.by, false)
			require.ErrorIs(t, err, tt.wantErr)

			if tt.wantMsg != "" {
				require.ErrorContains(t, err, tt.wantMsg)
			}
		})
	}
}

func TestUndoThatFailsForGood(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.changedMonitor(t, "call-1")
	h.monitor.undoErrs = []error{fmt.Errorf("the monitor is gone: %w", action.ErrPermanent)}

	_, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)

	failed, ok := errors.AsType[*UndoFailedError](err)
	require.True(t, ok, "got %v", err)
	require.Equal(t, action.StatusFailed, failed.Proposal.Status)
	require.Contains(t, failed.Proposal.Error, "the monitor is gone")
	require.Equal(t, "succeeded", h.row(t, id).Status, "the proposal stays as it was")

	undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
	require.NoError(t, err, "and can be undone again")
	require.Equal(t, action.StatusSucceeded, undo.Status)
}

func TestUndoThatFailsForNowIsRetriedByTheJob(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.changedMonitor(t, "call-1")
	h.monitor.undoErrs = []error{errBackendDown}

	undo, err := h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
	require.NoError(t, err)
	require.Equal(t, action.StatusExecuting, undo.Status)
	require.Contains(t, undo.Error, "the notes service is down")
	require.Equal(t, "succeeded", h.row(t, id).Status)

	ran, _ := h.runJob(t)
	require.False(t, ran, "the job waits until the request has surely ended")

	h.moveBackupJobsToNow(t)

	ran, err = h.runJob(t)
	require.True(t, ran)
	require.NoError(t, err, "the second attempt works")

	require.Equal(t, "succeeded", h.row(t, uuid.MustParse(undo.ID)).Status)
	require.Equal(t, "undone", h.row(t, id).Status)
	require.JSONEq(t, startMonitor, h.monitor.definition())
	require.Equal(t, []string{AuditUndoRequested, AuditSucceeded}, h.auditActions(t, uuid.MustParse(undo.ID)))
}

func TestUndoByTwoPeopleAtOnceRestoresOnce(t *testing.T) {
	t.Parallel()

	h := setup(t)
	id := h.changedMonitor(t, "call-1")

	errs := make([]error, 2)

	var wg sync.WaitGroup

	for i := range errs {
		wg.Go(func() {
			_, errs[i] = h.svc.Undo(t.Context(), id, Decider{UserID: anaUser}, false)
		})
	}

	wg.Wait()

	succeeded := 0

	for _, err := range errs {
		if err == nil {
			succeeded++

			continue
		}

		require.True(t, errors.Is(err, ErrUndoRunning) || errors.Is(err, ErrNotUndoable), "got %v", err)
	}

	require.Equal(t, 1, succeeded)
	require.Equal(t, 2, h.monitor.writeCount(), "the change and one restore")
	require.Equal(t, "undone", h.row(t, id).Status)
}

// moveBackupJobsToNow makes the execute_action jobs that wait ready now, as
// if UndoTimeout and the margin had passed.
func (h *harness) moveBackupJobsToNow(t *testing.T) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() WHERE kind = $1 AND status = 'pending'", queue.KindExecuteAction)
	require.NoError(t, err)
}

// undoAttempts returns the undo proposals of the proposal with id, oldest first.
func (h *harness) undoAttempts(t *testing.T, id uuid.UUID) []undoAttempt {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT id, status, error FROM action_proposals WHERE undo_of = $1 ORDER BY proposed_at, id", id)
	require.NoError(t, err)

	defer rows.Close()

	var out []undoAttempt

	for rows.Next() {
		var a undoAttempt
		require.NoError(t, rows.Scan(&a.ID, &a.Status, &a.Error))

		out = append(out, a)
	}

	require.NoError(t, rows.Err())

	return out
}

// undoAttempt is one undo proposal of a proposal.
type undoAttempt struct {
	ID     uuid.UUID
	Status string
	Error  *string
}

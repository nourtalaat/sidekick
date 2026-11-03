package argocdrollback

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// executed rolls the fake back one revision, and returns the action, the
// snapshot and the invocation of that rollback. The history then ends with
// entry 8, which holds revB.
func executed(t *testing.T) (*Rollback, *fakeArgo, action.Snapshot, action.Invocation) {
	t.Helper()

	api, f := newFakeArgo(t)
	a := newTestAction(t, api, nil)
	snap := snapshotOf(t, a, oneBack)
	inv := invocationOf(oneBack, &snap)

	_, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, []int64{6}, f.rolledBack())

	return a, f, snap, action.Invocation{ProposalID: "undo-1", Params: inv.Params, IdempotencyKey: "key-2", ApprovedBy: "user-1", UndoOf: "proposal-1"}
}

func TestUndoRollsBackToTheRevisionThatRanBefore(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)

	res, err := a.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)
	require.Equal(t, "Undid the rollback of application shop/checkout: it runs cccc333 again. Argo CD recorded it as deployment 9.", res.Summary)
	require.Equal(t, []int64{6, 7}, f.rolledBack(), "the second rollback names the entry that ran before the first")
	require.JSONEq(t, `{"id": 7, "project": "shop", "appNamespace": "argocd", "prune": false}`, string(f.bodies[1]))
}

func TestUndoTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)

	first, err := a.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)

	second, err := a.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)

	require.Equal(t, first.Summary, second.Summary)
	require.Equal(t, []int64{6, 7}, f.rolledBack())
}

func TestUndoKnowsItIsDoneWhenTheOlderEntryIsGone(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)

	// Argo CD keeps two entries here: the rollback, then the undo.
	f.update(func(f *fakeArgo) {
		f.deploy(9, revC, "sidekick")
		f.history = f.history[len(f.history)-2:]
	})

	res, err := a.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Undid the rollback")
	require.Equal(t, []int64{6}, f.rolledBack())
}

func TestUndoWaitsForTheOperationOfAnEarlierAttempt(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)

	f.update(func(f *fakeArgo) { f.operation, f.polls, f.pollsToEnd = revC, 1, 2 })

	res, err := a.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Undid the rollback")
	require.Equal(t, []int64{6}, f.rolledBack(), "the operation of the earlier attempt is waited for")
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(f *fakeArgo)
	}{
		{name: "deployed again with a new revision", change: func(f *fakeArgo) { f.deploy(9, revD, "bob") }},
		{name: "the rollback entry is not the newest and another is", change: func(f *fakeArgo) { f.history = f.history[:2] }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, f, snap, inv := executed(t)
			f.update(tt.change)

			_, err := a.Undo(t.Context(), inv, snap, false)
			require.ErrorIs(t, err, action.ErrConflict)
			require.Equal(t, []int64{6}, f.rolledBack(), "nothing is restored without force")

			ce, ok := errors.AsType[*action.ConflictError](err)
			require.True(t, ok, "got %v", err)

			var live action.Revision

			require.NoError(t, json.Unmarshal(ce.Live, &live))
			require.Equal(t, "Argo CD deployment", live.Kind)
			require.NotEmpty(t, live.ID)
		})
	}
}

func TestUndoWhenSomeoneRestoredTheRevisionAlready(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)
	f.deployAgain(revC)

	res, err := a.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "it runs cccc333 again")
	require.Equal(t, []int64{6}, f.rolledBack(), "the application runs the revision again, so nothing is asked for")
}

func TestUndoConflictShowsTheRevisionThatRunsNow(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)
	f.deployAgain(revD)

	_, err := a.Undo(t.Context(), inv, snap, false)

	ce, ok := errors.AsType[*action.ConflictError](err)
	require.True(t, ok, "got %v", err)

	var live action.Revision

	require.NoError(t, json.Unmarshal(ce.Live, &live))
	require.Equal(t, "9", live.ID)
	require.Equal(t, "dddd444", live.Version)
	require.Equal(t, "bob", live.DeployedBy)
}

func TestUndoWithForce(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)
	f.deployAgain(revD)

	res, err := a.Undo(t.Context(), inv, snap, true)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "it runs cccc333 again")
	require.Equal(t, []int64{6, 7}, f.rolledBack(), "force rolls back to the entry that ran before the first rollback")
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		change       func(f *fakeArgo)
		force        bool
		wantErr      string
		wantLimit    bool
		wantConflict bool
	}{
		{name: "auto-sync is on", change: func(f *fakeArgo) { f.autoSync = true }, wantErr: "refuses a rollback while auto-sync is on", wantLimit: true},
		{name: "auto-sync is on and force is set", change: func(f *fakeArgo) { f.autoSync = true }, force: true, wantErr: "refuses a rollback while auto-sync is on", wantLimit: true},
		{name: "several sources", change: func(f *fakeArgo) { f.sources = 2 }, wantErr: "has several sources", wantLimit: true},
		{name: "someone else's operation runs", change: func(f *fakeArgo) { f.operation, f.polls, f.pollsToEnd = revA, 1, -1 }, wantConflict: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, f, snap, inv := executed(t)
			f.update(tt.change)

			_, err := a.Undo(t.Context(), inv, snap, tt.force)
			require.Error(t, err)
			require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))
			require.Equal(t, tt.wantConflict, errors.Is(err, action.ErrConflict))

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			}

			require.Equal(t, []int64{6}, f.rolledBack())
		})
	}
}

func TestUndoWhenTheEntryThatRanBeforeIsGone(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)

	f.update(func(f *fakeArgo) { f.history = []map[string]any{f.history[0], f.history[1], f.history[3]} })

	_, err := a.Undo(t.Context(), inv, snap, false)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "history entry 7 of application shop/checkout is gone")
	require.ErrorContains(t, err, "revisionHistoryLimit")
	require.Equal(t, []int64{6}, f.rolledBack())
}

func TestUndoErrorsOfTheCalls(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		change        func(f *fakeArgo)
		wantPermanent bool
	}{
		{name: "the token may not sync", change: func(f *fakeArgo) { f.rollbackFailure = &fakeFailure{http.StatusForbidden, "permission denied"} }, wantPermanent: true},
		{name: "a server error", change: func(f *fakeArgo) { f.rollbackFailure = &fakeFailure{http.StatusInternalServerError, "boom"} }},
		{name: "the operation fails", change: func(f *fakeArgo) { f.phase, f.message = "Failed", "sync failed" }, wantPermanent: true},
		{name: "a read fails", change: func(f *fakeArgo) { f.getFailure = &fakeFailure{http.StatusBadGateway, "bad gateway"} }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a, f, snap, inv := executed(t)
			f.update(tt.change)

			_, err := a.Undo(t.Context(), inv, snap, false)
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
		})
	}
}

func TestUndoNeedsARollbackSnapshotOfTheApplication(t *testing.T) {
	t.Parallel()

	a, f, snap, inv := executed(t)

	other := snap
	other.Target = "argocd:application/shop/cart"

	notARollback := snap
	notARollback.Revision = false

	tests := []struct {
		name   string
		snap   action.Snapshot
		params json.RawMessage
	}{
		{name: "the zero snapshot"},
		{name: "a snapshot of another application", snap: other},
		{name: "a snapshot that is not a rollback's", snap: notARollback},
		{name: "an application the config does not allow", snap: snap, params: json.RawMessage(`{"application": "platform/ingress"}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			use := inv
			if tt.params != nil {
				use.Params = tt.params
			}

			_, err := a.Undo(t.Context(), use, tt.snap, false)
			require.Error(t, err)
			require.Equal(t, []int64{6}, f.rolledBack())
		})
	}
}

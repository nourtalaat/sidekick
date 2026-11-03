package awslambda

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// wantLink is the link to the alias in the Lambda console.
var wantLink = action.Link{
	Title: "Lambda alias live of function checkout",
	URL:   "https://us-east-1.console.aws.amazon.com/lambda/home?region=us-east-1#/functions/checkout/aliases/live",
}

// snapshotted returns the action, its fake Lambda and the Invocation of a
// proposal to roll back with params, whose snapshot the action made.
func snapshotted(t *testing.T, params string, edit func(*Config)) (*Rollback, *fakeLambda, action.Invocation) {
	t.Helper()

	r, f := newTestAction(t, edit)

	snap, err := r.Snapshot(t.Context(), json.RawMessage(params))
	require.NoError(t, err)

	f.forgetOps()

	return r, f, invocation(params, snap)
}

// hide makes a version unreadable, as if it was deleted.
func hide(f *fakeLambda, number int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	for i := range f.versions {
		if f.versions[i].number == number {
			f.versions[i].hidden = true
		}
	}
}

// aliasNow returns the version and split the fake alias has.
func (f *fakeLambda) aliasNow() (string, map[string]float64) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.version, f.weights
}

func TestExecute(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)

	res, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, action.Result{
		Summary: "Pointed alias live of function checkout at version 41 (it served version 42). The change is live now.",
		Links:   []action.Link{wantLink},
	}, res)

	require.Equal(t, []string{"GetAlias", "UpdateAlias"}, f.opsCalled(), "the target is not worked out again")

	bodies := f.updateBodies()
	require.Len(t, bodies, 1)
	require.JSONEq(t, `{"FunctionVersion":"41","RevisionId":"rev-1","RoutingConfig":{"AdditionalVersionWeights":{}}}`, string(bodies[0]),
		"the version, an empty split and the RevisionId of the snapshot, and no other change")

	version, weights := f.aliasNow()
	require.Equal(t, "41", version)
	require.Empty(t, weights)
}

func TestExecuteClearsASplit(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)
	f.set("42", map[string]float64{"43": 0.1})

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	res, err := r.Execute(t.Context(), invocation(paramsBack1, snap))
	require.NoError(t, err)
	require.Contains(t, res.Summary, "(it served version 42 with 10% of requests to version 43)")

	version, weights := f.aliasNow()
	require.Equal(t, "41", version)
	require.Empty(t, weights, "the split is gone")
}

func TestExecuteFailsWhenTheSplitIsNotCleared(t *testing.T) {
	t.Parallel()

	r, f := newTestAction(t, nil)
	f.set("42", map[string]float64{"43": 0.1})

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	f.keepsSplit = true

	_, err = r.Execute(t.Context(), invocation(paramsBack1, snap))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "alias live serves version 41 with traffic split map[43:0.1] after the update, not version 41 with split map[]")
}

func TestExecuteTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)

	_, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)

	res, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, "Alias live of function checkout already serves version 41, so nothing was changed.", res.Summary)
	require.Len(t, f.updateBodies(), 1)
}

func TestExecuteWhenTheAliasIsOnTheTargetAlready(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)
	f.set("41", nil)

	res, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "already serves version 41")
	require.Empty(t, f.updateBodies())
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		weights map[string]float64
		wantErr string
	}{
		{
			name: "a deploy of another version", version: "43",
			wantErr: "alias live of function checkout changed after the proposal (it served version 42, now it serves version 43)",
		},
		{
			name: "the same version put on the alias again", version: "42",
			wantErr: "(it served version 42, now it serves version 42)",
		},
		{
			name: "a split added by someone else", version: "42", weights: map[string]float64{"43": 0.1},
			wantErr: "now it serves version 42 with 10% of requests to version 43)",
		},
		{
			name: "a rollback to another version by hand", version: "40",
			wantErr: "now it serves version 40)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f, inv := snapshotted(t, paramsBack1, nil)
			f.set(tt.version, tt.weights)

			_, err := r.Execute(t.Context(), inv)
			require.ErrorIs(t, err, action.ErrConflict)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, f.updateBodies())
		})
	}
}

func TestExecuteConflictThroughTheRevisionID(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)
	f.changeBeforeUpdate = true

	_, err := r.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrConflict, "Lambda refused the RevisionId")
	require.ErrorContains(t, err, "alias live of function checkout changed after it was read")
	require.Len(t, f.updateBodies(), 1)

	version, _ := f.aliasNow()
	require.Equal(t, "42", version, "nothing was written")
}

func TestExecuteRefusals(t *testing.T) {
	t.Parallel()

	rollbackSnapshot := func(t *testing.T) action.Snapshot {
		t.Helper()

		_, _, inv := snapshotted(t, paramsBack1, nil)

		return *inv.Snapshot
	}

	tests := []struct {
		name      string
		params    string
		snapshot  func(*testing.T) *action.Snapshot
		fake      func(*fakeLambda)
		wantErr   string
		wantErrIs error
	}{
		{
			name: "no snapshot", params: paramsBack1, snapshot: func(*testing.T) *action.Snapshot { return nil },
			wantErr: "the proposal has no snapshot", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot of another alias", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)
				snap.Target = "aws:lambda/111122223333/us-east-1/cart-api/live"

				return &snap
			},
			wantErr: "the snapshot is of another alias", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot that is not a rollback", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)
				snap.Revision = false

				return &snap
			},
			wantErr: "the proposal has no function versions to restore", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot with no revision documents", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)
				snap.Before, snap.After = nil, nil

				return &snap
			},
			wantErr: "the snapshot cannot be read as two function versions", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot with no restore data", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)
				snap.Version = ""

				return &snap
			},
			wantErr: "the snapshot has no readable alias data to restore", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot with restore data that has no revision", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)
				snap.Version = `{"weights":{"43":0.1}}`

				return &snap
			},
			wantErr: "the snapshot has no alias revision to compare with", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot that names $LATEST", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)

				rolled, err := action.RevisionSnapshot(snap.Target, snap.Version,
					action.Revision{Kind: "Lambda version", ID: "42"}, action.Revision{Kind: "Lambda version", ID: "$LATEST"})
				require.NoError(t, err)

				return &rolled
			},
			wantErr: `serves "$LATEST", which is not a published version`, wantErrIs: action.ErrPermanent,
		},
		{
			name: "parameters outside the limits now", params: `{"function":"payments","alias":"live"}`,
			wantErr: `function "payments" is not one the action may change`, wantErrIs: action.ErrLimit,
		},
		{
			name: "an alias that was deleted", params: paramsBack1, fake: func(f *fakeLambda) { f.noAlias = true },
			wantErr: "has no alias live", wantErrIs: action.ErrPermanent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f, inv := snapshotted(t, paramsBack1, nil)

			inv.Params = json.RawMessage(tt.params)
			if tt.snapshot != nil {
				inv.Snapshot = tt.snapshot(t)
			}

			if tt.fake != nil {
				tt.fake(f)
			}

			_, err := r.Execute(t.Context(), inv)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, tt.wantErrIs)
			require.Empty(t, f.updateBodies())
		})
	}
}

func TestExecuteFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		op            string
		status        int
		code          string
		wantPermanent bool
		wantCalls     int
	}{
		{name: "an alias that was deleted", op: "UpdateAlias", status: http.StatusNotFound, code: "ResourceNotFoundException", wantPermanent: true, wantCalls: 1},
		{name: "a parameter Lambda rejects", op: "UpdateAlias", status: http.StatusBadRequest, code: "InvalidParameterValueException", wantPermanent: true, wantCalls: 1},
		{name: "a policy that refuses", op: "UpdateAlias", status: http.StatusForbidden, code: "AccessDeniedException", wantPermanent: true, wantCalls: 1},
		{name: "another operation in progress can be tried again", op: "UpdateAlias", status: http.StatusConflict, code: "ResourceConflictException", wantCalls: 1},
		{name: "a server error is tried once by the SDK and again by Sidekick", op: "UpdateAlias", status: http.StatusInternalServerError, code: "ServiceException", wantCalls: 1},
		{name: "a rate limit is tried once by the SDK and again by Sidekick", op: "UpdateAlias", status: http.StatusTooManyRequests, code: "TooManyRequestsException", wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f, inv := snapshotted(t, paramsBack1, nil)
			f.failWith(tt.op, tt.status, tt.code)

			_, err := r.Execute(t.Context(), inv)
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.NotErrorIs(t, err, action.ErrConflict)
			require.Equal(t, tt.wantCalls, f.count(tt.op), "the write is not repeated by the SDK")
		})
	}
}

func TestExecuteReadsAreRepeatedByTheSDK(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)
	f.failWith("GetAlias", http.StatusInternalServerError, "ServiceException")

	_, err := r.Execute(t.Context(), inv)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent)
	require.Equal(t, 3, f.count("GetAlias"))
	require.Empty(t, f.updateBodies())
}

// undone returns the action, its fake Lambda and the Invocation of the undo
// of a rollback that ran: the alias serves version 41. edit changes the
// config, and split is the traffic split the alias had before the rollback.
func undone(t *testing.T, edit func(*Config), split map[string]float64) (*Rollback, *fakeLambda, action.Invocation) {
	t.Helper()

	r, f := newTestAction(t, edit)
	f.set("42", split)

	snap, err := r.Snapshot(t.Context(), json.RawMessage(paramsBack1))
	require.NoError(t, err)

	inv := invocation(paramsBack1, snap)

	_, err = r.Execute(t.Context(), inv)
	require.NoError(t, err)

	inv.UndoOf = inv.ProposalID

	return r, f, inv
}

func TestUndo(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t, nil, nil)

	res, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err)
	require.Equal(t, action.Result{
		Summary: "Pointed alias live of function checkout at version 42 (it served version 41). The change is live now.",
		Links:   []action.Link{wantLink},
	}, res)

	bodies := f.updateBodies()
	require.Len(t, bodies, 2)
	require.JSONEq(t, `{"FunctionVersion":"42","RevisionId":"rev-3","RoutingConfig":{"AdditionalVersionWeights":{}}}`, string(bodies[1]),
		"the RevisionId is the one the alias had just before the undo")

	version, weights := f.aliasNow()
	require.Equal(t, "42", version)
	require.Empty(t, weights)
}

func TestUndoRestoresTheSplit(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t, nil, map[string]float64{"43": 0.25})

	res, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Pointed alias live of function checkout at version 42")

	bodies := f.updateBodies()
	require.Len(t, bodies, 2)
	require.JSONEq(t, `{"FunctionVersion":"42","RevisionId":"rev-3","RoutingConfig":{"AdditionalVersionWeights":{"43":0.25}}}`, string(bodies[1]))

	version, weights := f.aliasNow()
	require.Equal(t, "42", version)
	require.Equal(t, map[string]float64{"43": 0.25}, weights)
}

func TestUndoTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t, nil, map[string]float64{"43": 0.25})

	_, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err)

	res, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "already serves version 42 with 25% of requests to version 43")
	require.Len(t, f.updateBodies(), 2, "one for the rollback, one for the undo")
}

func TestUndoIsNotStoppedByALoweredLimit(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t, nil, nil)
	r.MaxRevisionsBack = new(1)

	inv.Params = json.RawMessage(paramsBack2)

	_, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err, "an undo puts back a change that was allowed when it was made")
	require.Len(t, f.updateBodies(), 2)
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		version string
		weights map[string]float64
	}{
		{name: "another version", version: "43"},
		{name: "the version of the rollback with a split added", version: "41", weights: map[string]float64{"43": 0.5}},
	}

	for _, tt := range tests {
		t.Run(tt.name+" without force writes nothing and shows the live version", func(t *testing.T) {
			t.Parallel()

			r, f, inv := undone(t, nil, nil)
			f.set(tt.version, tt.weights)

			_, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
			require.ErrorIs(t, err, action.ErrConflict)

			conflict, ok := errors.AsType[*action.ConflictError](err)
			require.True(t, ok)

			var live map[string]any
			require.NoError(t, json.Unmarshal(conflict.Live, &live))
			require.Equal(t, "revision", live["sidekickKind"])
			require.Equal(t, tt.version, live["id"])
			require.Equal(t, "Lambda version", live["kind"])
			require.Len(t, f.updateBodies(), 1, "only the rollback wrote")
		})

		t.Run(tt.name+" with force restores", func(t *testing.T) {
			t.Parallel()

			r, f, inv := undone(t, nil, nil)
			f.set(tt.version, tt.weights)

			res, err := r.Undo(t.Context(), inv, *inv.Snapshot, true)
			require.NoError(t, err)
			require.Contains(t, res.Summary, "Pointed alias live of function checkout at version 42 (it served version "+tt.version)

			version, weights := f.aliasNow()
			require.Equal(t, "42", version)
			require.Empty(t, weights)
		})
	}
}

func TestUndoConflictThroughTheRevisionID(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t, nil, nil)
	f.changeBeforeUpdate = true

	_, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.ErrorIs(t, err, action.ErrConflict)

	_, ok := errors.AsType[*action.ConflictError](err)
	require.True(t, ok, "the approver is shown the live alias")

	version, _ := f.aliasNow()
	require.Equal(t, "41", version, "nothing was written")
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fake    func(*fakeLambda)
		snap    func(*action.Snapshot)
		wantErr string
	}{
		{
			name: "a version that was deleted", fake: func(f *fakeLambda) { hide(f, 42) },
			wantErr: "version 42 of Lambda function checkout does not exist, so the alias cannot be put on it",
		},
		{name: "a snapshot that is not a rollback", snap: func(s *action.Snapshot) { s.Revision = false }, wantErr: "the proposal has no function versions to restore"},
		{name: "a snapshot with no restore data", snap: func(s *action.Snapshot) { s.Version = "" }, wantErr: "the snapshot has no readable alias data to restore"},
		{name: "an alias that was deleted", fake: func(f *fakeLambda) { f.noAlias = true }, wantErr: "has no alias live"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f, inv := undone(t, nil, nil)
			snap := *inv.Snapshot

			if tt.fake != nil {
				tt.fake(f)
			}

			if tt.snap != nil {
				tt.snap(&snap)
			}

			_, err := r.Undo(t.Context(), inv, snap, false)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Len(t, f.updateBodies(), 1, "only the rollback wrote")
		})
	}
}

func TestUndoWithNoSnapshotAtAll(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t, nil, nil)

	_, err := r.Undo(t.Context(), inv, action.Snapshot{}, false)
	require.ErrorIs(t, err, action.ErrPermanent, "the zero snapshot is a refusal, not a panic")
	require.Len(t, f.updateBodies(), 1)
}

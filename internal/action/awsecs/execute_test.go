package awsecs

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// wantLink is the link to the service in the ECS console.
var wantLink = action.Link{
	Title: "ECS service checkout in cluster shop",
	URL:   "https://us-east-1.console.aws.amazon.com/ecs/v2/clusters/shop/services/checkout/health?region=us-east-1",
}

// snapshotted returns the action, its fake ECS and the Invocation of a
// proposal to roll back with params, whose snapshot the action made.
func snapshotted(t *testing.T, params string, edit func(*Config)) (*Rollback, *fakeECS, action.Invocation) {
	t.Helper()

	r, f := newTestAction(t, edit)

	snap, err := r.Snapshot(t.Context(), json.RawMessage(params))
	require.NoError(t, err)

	f.mu.Lock()
	f.ops = nil
	f.mu.Unlock()

	return r, f, invocation(params, snap)
}

func TestExecute(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)

	res, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, action.Result{
		Summary: "Put service checkout of cluster shop back on task definition checkout:41 (it ran checkout:42). " +
			"The new deployment is ecs-svc/101. Sidekick does not wait for the rollout; the watcher reports whether the alert recovers.",
		Links: []action.Link{wantLink},
	}, res)

	require.Equal(t, []string{"DescribeServices", "UpdateService"}, f.ops, "the target is not worked out again")

	bodies := f.updateBodies()
	require.Len(t, bodies, 1)
	require.JSONEq(t, `{"cluster":"shop","service":"checkout","taskDefinition":"checkout:41"}`, string(bodies[0]),
		"the task definition and nothing else: no desired count, no forced deployment")
	require.Equal(t, "checkout:41", f.service.primary)
}

func TestExecuteTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)

	_, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)

	res, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, "Service checkout of cluster shop already runs task definition checkout:41 in deployment ecs-svc/101, so nothing was changed.", res.Summary)
	require.Len(t, f.updateBodies(), 1)
}

func TestExecuteWhenTheServiceIsOnTheTargetAlready(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)
	f.setPrimary("checkout:41", 101)

	res, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "already runs task definition checkout:41")
	require.Empty(t, f.updateBodies())
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		taskDef    string
		deployment int
		wantErr    string
	}{
		{
			name: "a deploy of another task definition", taskDef: "checkout:43", deployment: 101,
			wantErr: "service checkout in cluster shop changed after the proposal (it ran checkout:42 in deployment ecs-svc/100, now it runs checkout:43 in deployment ecs-svc/101)",
		},
		{
			name: "a new deployment of the same task definition", taskDef: "checkout:42", deployment: 101,
			wantErr: "(it ran checkout:42 in deployment ecs-svc/100, now it runs checkout:42 in deployment ecs-svc/101)",
		},
		{
			name: "a rollback to another revision by hand", taskDef: "checkout:40", deployment: 101,
			wantErr: "now it runs checkout:40 in deployment ecs-svc/101",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f, inv := snapshotted(t, paramsBack1, nil)
			f.setPrimary(tt.taskDef, tt.deployment)

			_, err := r.Execute(t.Context(), inv)
			require.ErrorIs(t, err, action.ErrConflict)
			require.ErrorContains(t, err, tt.wantErr)
			require.Empty(t, f.updateBodies())
		})
	}
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
		fake      func(*fakeECS)
		wantErr   string
		wantErrIs error
	}{
		{
			name: "no snapshot", params: paramsBack1, snapshot: func(*testing.T) *action.Snapshot { return nil },
			wantErr: "the proposal has no snapshot", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot of another service", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)
				snap.Target = "aws:ecs/111122223333/us-east-1/shop/cart-api"

				return &snap
			},
			wantErr: "the snapshot is of another service", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a snapshot that is not a rollback", params: paramsBack1,
			snapshot: func(t *testing.T) *action.Snapshot {
				snap := rollbackSnapshot(t)
				snap.Revision = false

				return &snap
			},
			wantErr: "the proposal has no task definition revisions to restore", wantErrIs: action.ErrPermanent,
		},
		{
			name: "parameters outside the limits now", params: `{"cluster":"prod","service":"checkout"}`,
			wantErr: `cluster "prod" is not one the action may change`, wantErrIs: action.ErrLimit,
		},
		{
			name: "a service that moved to CodeDeploy", params: paramsBack1, fake: func(f *fakeECS) { f.service.controller = "CODE_DEPLOY" },
			wantErr: "uses the CODE_DEPLOY deployment controller", wantErrIs: action.ErrPermanent,
		},
		{
			name: "a service that was deleted", params: paramsBack1, fake: func(f *fakeECS) { f.noService = true },
			wantErr: "has no service checkout", wantErrIs: action.ErrPermanent,
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
		{name: "a service that became inactive", op: "UpdateService", status: http.StatusBadRequest, code: "ServiceNotActiveException", wantPermanent: true, wantCalls: 1},
		{name: "a policy that refuses", op: "UpdateService", status: http.StatusBadRequest, code: "AccessDeniedException", wantPermanent: true, wantCalls: 1},
		{name: "a server error is tried once by the SDK and again by Sidekick", op: "UpdateService", status: http.StatusInternalServerError, code: "ServerException", wantCalls: 1},
		{name: "a rate limit is tried once by the SDK and again by Sidekick", op: "UpdateService", status: http.StatusBadRequest, code: "ThrottlingException", wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f, inv := snapshotted(t, paramsBack1, nil)
			f.failWith(tt.op, tt.status, tt.code)

			_, err := r.Execute(t.Context(), inv)
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.Equal(t, tt.wantCalls, f.count(tt.op), "the write is not repeated by the SDK")
		})
	}
}

func TestExecuteReadsAreRepeatedByTheSDK(t *testing.T) {
	t.Parallel()

	r, f, inv := snapshotted(t, paramsBack1, nil)
	f.failWith("DescribeServices", http.StatusInternalServerError, "ServerException")

	_, err := r.Execute(t.Context(), inv)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent)
	require.Equal(t, 3, f.count("DescribeServices"))
	require.Empty(t, f.updateBodies())
}

// undone returns the action, its fake ECS and the Invocation of the undo of a
// rollback that ran: the service is on checkout:41.
func undone(t *testing.T) (*Rollback, *fakeECS, action.Invocation) {
	t.Helper()

	r, f, inv := snapshotted(t, paramsBack1, nil)

	_, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)

	inv.UndoOf = inv.ProposalID

	return r, f, inv
}

func TestUndo(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t)

	res, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err)
	require.Equal(t, action.Result{
		Summary: "Put service checkout of cluster shop back on task definition checkout:42 (it ran checkout:41). " +
			"The new deployment is ecs-svc/102. Sidekick does not wait for the rollout; the watcher reports whether the alert recovers.",
		Links: []action.Link{wantLink},
	}, res)

	bodies := f.updateBodies()
	require.Len(t, bodies, 2)
	require.JSONEq(t, `{"cluster":"shop","service":"checkout","taskDefinition":"checkout:42"}`, string(bodies[1]))
	require.Equal(t, "checkout:42", f.service.primary)
}

func TestUndoTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	r, f, inv := undone(t)

	_, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err)

	res, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "already runs task definition checkout:42")
	require.Len(t, f.updateBodies(), 2, "one for the rollback, one for the undo")
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	t.Run("without force it writes nothing and shows the live task definition", func(t *testing.T) {
		t.Parallel()

		r, f, inv := undone(t)
		f.setPrimary("checkout:43", 200)

		_, err := r.Undo(t.Context(), inv, *inv.Snapshot, false)
		require.ErrorIs(t, err, action.ErrConflict)

		conflict, ok := errors.AsType[*action.ConflictError](err)
		require.True(t, ok)

		var live map[string]any
		require.NoError(t, json.Unmarshal(conflict.Live, &live))
		require.Equal(t, "revision", live["sidekickKind"])
		require.Equal(t, "checkout:43", live["id"])
		require.Equal(t, imageFor(43), live["version"])
		require.Len(t, f.updateBodies(), 1, "only the rollback wrote")
	})

	t.Run("with force it restores", func(t *testing.T) {
		t.Parallel()

		r, f, inv := undone(t)
		f.setPrimary("checkout:43", 200)

		res, err := r.Undo(t.Context(), inv, *inv.Snapshot, true)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "back on task definition checkout:42 (it ran checkout:43)")
		require.Equal(t, "checkout:42", f.service.primary)
	})
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		fake    func(*fakeECS)
		snap    func(*action.Snapshot)
		wantErr string
	}{
		{
			name: "a task definition that is not ACTIVE now", fake: func(f *fakeECS) { f.describeStatus["checkout:42"] = "INACTIVE" },
			wantErr: "task definition checkout:42 is INACTIVE, so Undo cannot put the service back on it",
		},
		{
			name: "a task definition ECS cannot describe", fake: func(f *fakeECS) { f.definitions[1].hidden = true },
			wantErr: "task definition checkout:42 cannot be read from ECS, so Undo cannot put the service back on it",
		},
		{name: "a snapshot that is not a rollback", snap: func(s *action.Snapshot) { s.Revision = false }, wantErr: "the proposal has no task definition revisions to restore"},
		{name: "a service on another controller", fake: func(f *fakeECS) { f.service.controller = "EXTERNAL" }, wantErr: "uses the EXTERNAL deployment controller"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r, f, inv := undone(t)
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

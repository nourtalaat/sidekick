package gcprun

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// executed makes the rollback of the test service and returns what Undo needs.
func executed(t *testing.T) (*fakeRun, *Rollback, action.Snapshot) {
	t.Helper()

	_, run, a, snap := setup(t)

	_, err := a.Execute(context.Background(), invocation(&snap))
	require.NoError(t, err)

	return run, a, snap
}

func TestUndoRestoresTheLatestRevision(t *testing.T) {
	t.Parallel()

	run, a, snap := executed(t)

	res, err := a.Undo(context.Background(), invocation(&snap), snap, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "all of it follows the latest ready revision again, so deploys get traffic")

	require.Equal(t, 2, run.patchCount())
	require.JSONEq(t, jsonText(latestTraffic), run.trafficNow())

	patch := run.calls()[1]
	require.Equal(t, "traffic", patch.query.Get("updateMask"))
	require.Equal(t, "etag-2", patch.body["etag"], "the etag that was read just now is sent")
	require.Equal(t, []any{map[string]any{"type": allocationLatest, "percent": float64(100)}}, patch.body["traffic"])
}

func TestUndoRestoresTheNamedRevision(t *testing.T) {
	t.Parallel()

	_, run, a, _ := setup(t)
	run.update(func(f *fakeRun) { f.traffic = pinnedTraffic("checkout-00007-aaa") })
	snap := snapshotOf(t, a, paramsJSON)

	_, err := a.Execute(context.Background(), invocation(&snap))
	require.NoError(t, err)
	require.JSONEq(t, jsonText(pinnedTraffic("checkout-00005-yyy")), run.trafficNow())

	res, err := a.Undo(context.Background(), invocation(&snap), snap, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "all of it goes to revision checkout-00007-aaa.")
	require.JSONEq(t, jsonText(pinnedTraffic("checkout-00007-aaa")), run.trafficNow())
}

func TestUndoTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	run, a, snap := executed(t)

	_, err := a.Undo(context.Background(), invocation(&snap), snap, false)
	require.NoError(t, err)

	res, err := a.Undo(context.Background(), invocation(&snap), snap, false)
	require.NoError(t, err)
	require.Equal(t, "Traffic of service checkout is back as it was before the rollback.", res.Summary)
	require.Equal(t, 2, run.patchCount(), "one for the rollback and one for the undo")
}

func TestUndoAfterADeployStillWorks(t *testing.T) {
	t.Parallel()

	run, a, snap := executed(t)
	run.deploy()

	_, err := a.Undo(context.Background(), invocation(&snap), snap, false)
	require.NoError(t, err, "a deploy leaves the traffic as the rollback wrote it, so there is nothing to confirm")
	require.JSONEq(t, jsonText(latestTraffic), run.trafficNow())
	require.Equal(t, "etag-3", run.calls()[1].body["etag"])
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		traffic  []map[string]any
		wantID   string
		wantNote string
	}{
		{name: "traffic set to another revision", traffic: pinnedTraffic("checkout-00004-xxx"), wantID: "checkout-00004-xxx", wantNote: "Traffic is pinned to this revision by name."},
		{
			name: "traffic split",
			traffic: []map[string]any{
				{"type": allocationRevision, "revision": "checkout-00004-xxx", "percent": 60},
				{"type": allocationRevision, "revision": "checkout-00005-yyy", "percent": 40},
			},
			wantID:   "checkout-00004-xxx 60 percent, checkout-00005-yyy 40 percent",
			wantNote: "The traffic of the service is split, or has entries with a tag or no traffic.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			run, a, snap := executed(t)
			run.setTraffic(tt.traffic)

			_, err := a.Undo(context.Background(), invocation(&snap), snap, false)
			require.ErrorIs(t, err, action.ErrConflict)

			var conflict *action.ConflictError

			require.ErrorAs(t, err, &conflict)

			var live struct {
				SidekickKind string `json:"sidekickKind"`
				Kind         string `json:"kind"`
				ID           string `json:"id"`
				Notes        string `json:"notes"`
			}

			require.NoError(t, json.Unmarshal(conflict.Live, &live))
			require.Equal(t, "revision", live.SidekickKind)
			require.Equal(t, revisionKind, live.Kind)
			require.Equal(t, tt.wantID, live.ID)
			require.Contains(t, live.Notes, tt.wantNote)
			require.Equal(t, 1, run.patchCount(), "the undo wrote nothing")

			_, err = a.Undo(context.Background(), invocation(&snap), snap, true)
			require.NoError(t, err, "force restores anyway")
			require.JSONEq(t, jsonText(latestTraffic), run.trafficNow())
			require.Equal(t, 2, run.patchCount())
		})
	}
}

func TestUndoRefusesWhatIsNotRight(t *testing.T) {
	t.Parallel()

	_, a, snap := executed(t)

	badVersion := snap
	badVersion.Version = "unknown"

	otherService := snap
	otherService.Target = "gcp:cloudrun/shop-prod/europe-west1/payments"

	tests := []struct {
		name string
		snap action.Snapshot
		want string
	}{
		{name: "a version that is not a type and an etag", snap: badVersion, want: errBadSnapshot.Error()},
		{name: "a snapshot of another service", snap: otherService, want: errTargetDiffer.Error()},
		{name: "an empty snapshot", snap: action.Snapshot{}, want: errBadSnapshot.Error()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := a.Undo(context.Background(), invocation(&tt.snap), tt.snap, false)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestUndoChecksTheLimitsAgain(t *testing.T) {
	t.Parallel()

	_, a, snap := executed(t)

	inv := invocation(&snap)
	inv.Params = json.RawMessage(`{"service":"orders"}`)

	_, err := a.Undo(context.Background(), inv, snap, false)
	require.ErrorIs(t, err, action.ErrLimit)
}

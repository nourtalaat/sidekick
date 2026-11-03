package gcprun

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// setup makes a fake service and an action for it, and takes the snapshot of
// a rollback one revision back.
func setup(t *testing.T) (*fakehttp.Server, *fakeRun, *Rollback, action.Snapshot) {
	t.Helper()

	api := fakehttp.New(t)
	run := newFakeRun(t, api)
	a := buildAction(t, api, limitedConfig())

	return api, run, a, snapshotOf(t, a, paramsJSON)
}

func TestExecute(t *testing.T) {
	t.Parallel()

	api, run, a, snap := setup(t)
	run.update(func(f *fakeRun) { f.pollsToDone = 2 })

	res, err := a.Execute(context.Background(), invocation(&snap))
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Sent all traffic of service checkout to revision checkout-00007-aaa.")
	require.Contains(t, res.Summary, "The service is pinned to it: later deploys get no traffic until traffic is set back to the latest revision")
	require.Equal(t, "https://console.cloud.google.com/run/detail/europe-west1/checkout/revisions?project=shop-prod", res.Links[0].URL)

	require.Len(t, run.calls(), 1)

	patch := run.calls()[0]
	require.Equal(t, "traffic", patch.query.Get("updateMask"))
	require.Equal(t, "etag-1", patch.body["etag"], "the etag that was read is sent")
	require.Equal(t, []any{map[string]any{"type": allocationRevision, "revision": "checkout-00007-aaa", "percent": float64(100)}}, patch.body["traffic"])
	require.Contains(t, patch.body, "template", "the service is sent back as it was read")
	require.Equal(t, "projects/shop-prod/locations/europe-west1/services/checkout", patch.body["name"])
	require.JSONEq(t, jsonText(pinnedTraffic("checkout-00007-aaa")), run.trafficNow())

	polls := 0

	for _, r := range api.Requests() {
		require.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))

		if r.Path == operationPath {
			polls++
		}
	}

	require.Equal(t, 3, polls, "two answers that are not done, then one that is")
}

func TestExecuteAPinnedServiceStaysPinned(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	run := newFakeRun(t, api)
	run.update(func(f *fakeRun) { f.traffic = pinnedTraffic("checkout-00007-aaa") })
	a := buildAction(t, api, limitedConfig())
	snap := snapshotOf(t, a, paramsJSON)

	res, err := a.Execute(context.Background(), invocation(&snap))
	require.NoError(t, err)
	require.Contains(t, res.Summary, "to revision checkout-00005-yyy.")
	require.Contains(t, res.Summary, "The service stays pinned to a named revision.")
	require.JSONEq(t, jsonText(pinnedTraffic("checkout-00005-yyy")), run.trafficNow())
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*fakeRun)
	}{
		{name: "a deploy after the proposal", change: func(f *fakeRun) { f.deploy() }},
		{name: "an edit after the proposal", change: func(f *fakeRun) { f.update(func(f *fakeRun) { f.etag++ }) }},
		{name: "traffic set to another revision", change: func(f *fakeRun) { f.setTraffic(pinnedTraffic("checkout-00005-yyy")) }},
		{name: "traffic split", change: func(f *fakeRun) {
			f.setTraffic([]map[string]any{
				{"type": allocationLatest, "percent": 50},
				{"type": allocationRevision, "revision": "checkout-00007-aaa", "percent": 50},
			})
		}},
		{name: "the latest revision changed with the same etag", change: func(f *fakeRun) { f.update(func(f *fakeRun) { f.latestReady = "checkout-00009-ccc" }) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, run, a, snap := setup(t)
			tt.change(run)

			_, err := a.Execute(context.Background(), invocation(&snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.Zero(t, run.patchCount(), "nothing is written")
		})
	}
}

func TestExecuteTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	_, run, a, snap := setup(t)
	inv := invocation(&snap)

	_, err := a.Execute(context.Background(), inv)
	require.NoError(t, err)

	res, err := a.Execute(context.Background(), inv)
	require.NoError(t, err)
	require.Equal(t, "Traffic of service checkout already goes to revision checkout-00007-aaa.", res.Summary)
	require.Equal(t, 1, run.patchCount())
}

func TestExecuteAfterACrash(t *testing.T) {
	t.Parallel()

	_, run, a, snap := setup(t)
	run.update(func(f *fakeRun) { f.pollFailures = 1 })
	inv := invocation(&snap)

	_, err := a.Execute(context.Background(), inv)
	require.ErrorContains(t, err, "wait for the update")
	require.NotErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrConflict, "Sidekick tries again")
	require.Equal(t, 1, run.patchCount())

	_, err = a.Execute(context.Background(), inv)
	require.NoError(t, err)
	require.Equal(t, 1, run.patchCount(), "the retry finds the traffic changed and writes nothing")
}

func TestExecuteAnswers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     func(*fakeRun)
		wantIs    error
		wantNotIs error
		want      string
	}{
		{name: "the etag does not match", setup: func(f *fakeRun) { f.patchStatus = http.StatusConflict }, wantIs: action.ErrConflict, want: "Google Cloud answered 409"},
		{name: "the token may not update the service", setup: func(f *fakeRun) { f.patchStatus = http.StatusForbidden }, wantIs: action.ErrPermanent, want: "Google Cloud answered 403"},
		{name: "the revision is refused", setup: func(f *fakeRun) { f.patchStatus = http.StatusBadRequest }, wantIs: action.ErrPermanent, want: "Google Cloud answered 400"},
		{name: "Google Cloud fails", setup: func(f *fakeRun) { f.patchStatus = http.StatusServiceUnavailable }, wantNotIs: action.ErrPermanent, want: "Google Cloud answered 503"},
		{name: "the operation is refused for good", setup: func(f *fakeRun) { f.opError = map[string]any{"code": 5, "message": "revision not found"} }, wantIs: action.ErrPermanent, want: "revision not found"},
		{name: "the operation is aborted", setup: func(f *fakeRun) { f.opError = map[string]any{"code": 10, "message": "changed"} }, wantIs: action.ErrConflict, want: "changed"},
		{name: "the operation fails for a reason that may pass", setup: func(f *fakeRun) { f.opError = map[string]any{"code": 14, "message": "unavailable"} }, wantNotIs: action.ErrPermanent, want: "unavailable"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, run, a, snap := setup(t)
			run.update(tt.setup)

			_, err := a.Execute(context.Background(), invocation(&snap))
			require.ErrorContains(t, err, tt.want)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}

			if tt.wantNotIs != nil {
				require.NotErrorIs(t, err, tt.wantNotIs)
				require.NotErrorIs(t, err, action.ErrConflict)
			}
		})
	}
}

// waitLimit is how long the test lets Execute poll an operation that never ends.
const waitLimit = 100 * time.Millisecond

func TestExecuteWaitEndsWithTheContext(t *testing.T) {
	t.Parallel()

	_, run, a, snap := setup(t)
	run.update(func(f *fakeRun) { f.pollsToDone = 1 << 20 })

	ctx, cancel := context.WithTimeout(context.Background(), waitLimit)
	defer cancel()

	_, err := a.Execute(ctx, invocation(&snap))
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestExecuteRefusesWhatIsNotRight(t *testing.T) {
	t.Parallel()

	_, _, a, snap := setup(t)

	otherService := snap
	otherService.Target = "gcp:cloudrun/shop-prod/europe-west1/payments"

	badVersion := snap
	badVersion.Version = "etag-1"

	notARollback := snap
	notARollback.Revision = false

	outside := invocation(&snap)
	outside.Params = json.RawMessage(`{"service":"orders"}`)

	tests := []struct {
		name   string
		inv    action.Invocation
		wantIs error
		want   string
	}{
		{name: "no snapshot", inv: invocation(nil), wantIs: action.ErrPermanent, want: errNoSnapshot.Error()},
		{name: "a snapshot of another service", inv: invocation(&otherService), wantIs: action.ErrPermanent, want: errTargetDiffer.Error()},
		{name: "a version that is not a type and an etag", inv: invocation(&badVersion), wantIs: action.ErrPermanent, want: errBadSnapshot.Error()},
		{name: "a snapshot that is not a rollback", inv: invocation(&notARollback), wantIs: action.ErrPermanent, want: errBadSnapshot.Error()},
		{name: "parameters outside the limits", inv: outside, wantIs: action.ErrLimit, want: "not one of limits.services"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := a.Execute(context.Background(), tt.inv)
			require.ErrorIs(t, err, tt.wantIs)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestExecuteWhenTheServiceIsGone(t *testing.T) {
	t.Parallel()

	_, run, a, snap := setup(t)
	run.update(func(f *fakeRun) { f.missing = true })

	_, err := a.Execute(context.Background(), invocation(&snap))
	require.ErrorIs(t, err, action.ErrPermanent)

	var se *statusError

	require.ErrorAs(t, err, &se)
	require.Equal(t, http.StatusNotFound, se.code)
}

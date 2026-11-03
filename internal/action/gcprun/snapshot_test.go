package gcprun

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

const testTarget = "gcp:cloudrun/shop-prod/europe-west1/checkout"

func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		max         int
		back        int
		setup       func(*fakeRun)
		wantVersion string
		wantRunning string
		wantTarget  string
		wantImage   string
		wantBy      string
		runNotes    string
		targetNotes string
	}{
		{
			name: "one back from a service that follows the latest revision", max: 1, back: 1,
			wantVersion: "latest etag-1", wantRunning: "checkout-00008-bbb", wantTarget: "checkout-00007-aaa",
			wantImage: "registry.example.com/checkout:1.4.7", wantBy: "bo@example.com",
			runNotes:    "Traffic follows the latest ready revision. The service was last changed by ana@example.com, who may not be the one who made this revision.",
			targetNotes: "Pins the service to this revision: new deploys get no traffic until traffic is set back to the latest revision. Undo does that.",
		},
		{
			name: "two back skips a revision that is not ready", max: 2, back: 2,
			wantVersion: "latest etag-1", wantRunning: "checkout-00008-bbb", wantTarget: "checkout-00005-yyy",
			wantImage: "registry.example.com/checkout:1.4.5", wantBy: "",
			runNotes:    "Traffic follows the latest ready revision. The service was last changed by ana@example.com, who may not be the one who made this revision.",
			targetNotes: "Pins the service to this revision: new deploys get no traffic until traffic is set back to the latest revision. Undo does that.",
		},
		{
			name: "three back is the oldest revision", max: 3, back: 3,
			wantVersion: "latest etag-1", wantRunning: "checkout-00008-bbb", wantTarget: "checkout-00004-xxx",
			wantImage: "registry.example.com/checkout:1.4.4", wantBy: "bo@example.com",
			runNotes:    "Traffic follows the latest ready revision. The service was last changed by ana@example.com, who may not be the one who made this revision.",
			targetNotes: "Pins the service to this revision: new deploys get no traffic until traffic is set back to the latest revision. Undo does that.",
		},
		{
			name: "a service that is pinned already", max: 1, back: 1,
			setup:       func(f *fakeRun) { f.traffic = pinnedTraffic("checkout-00007-aaa"); f.lastModifier = "" },
			wantVersion: "pinned etag-1", wantRunning: "checkout-00007-aaa", wantTarget: "checkout-00005-yyy",
			wantImage: "registry.example.com/checkout:1.4.5", wantBy: "",
			runNotes:    "Traffic is pinned to this revision by name.",
			targetNotes: "The service stays pinned to a named revision, as it is now.",
		},
		{
			name: "a service with no traffic list follows the latest revision", max: 1, back: 1,
			setup:       func(f *fakeRun) { f.traffic = nil },
			wantVersion: "latest etag-1", wantRunning: "checkout-00008-bbb", wantTarget: "checkout-00007-aaa",
			wantImage: "registry.example.com/checkout:1.4.7", wantBy: "bo@example.com",
			runNotes:    "Traffic follows the latest ready revision. The service was last changed by ana@example.com, who may not be the one who made this revision.",
			targetNotes: "Pins the service to this revision: new deploys get no traffic until traffic is set back to the latest revision. Undo does that.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			run := newFakeRun(t, api)

			if tt.setup != nil {
				run.update(tt.setup)
			}

			cfg := limitedConfig()
			cfg.MaxRevisionsBack = new(tt.max)

			snap := snapshotOf(t, buildAction(t, api, cfg), fmt.Sprintf(`{"service":"checkout","revisionsBack":%d}`, tt.back))

			require.Equal(t, testTarget, snap.Target)
			require.Equal(t, tt.wantVersion, snap.Version)
			require.True(t, snap.Revision)

			revs, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, tt.wantRunning, revs.Running.ID)
			require.Equal(t, tt.wantTarget, revs.GoesBackTo.ID)
			require.Equal(t, revisionKind, revs.Running.Kind)
			require.Equal(t, tt.runNotes, revs.Running.Notes)
			require.Equal(t, tt.targetNotes, revs.GoesBackTo.Notes)
			require.Equal(t, tt.wantImage, revs.GoesBackTo.Version)
			require.Equal(t, tt.wantBy, revs.GoesBackTo.DeployedBy)
			require.NotNil(t, revs.GoesBackTo.DeployedAt)

			changes, err := action.Changes(snap)
			require.NoError(t, err)
			require.NotEmpty(t, changes)

			require.True(t, onlyGets(api), "Snapshot changes nothing")

			for _, r := range api.Requests() {
				require.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))
			}
		})
	}
}

func TestSnapshotRevisionFields(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	newFakeRun(t, api)

	snap := snapshotOf(t, buildAction(t, api, limitedConfig()), paramsJSON)

	revs, err := snap.Revisions()
	require.NoError(t, err)
	require.Equal(t, "registry.example.com/checkout:1.4.8", revs.Running.Version)
	require.Equal(t, "ana@example.com", revs.Running.DeployedBy)
	require.Equal(t, time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC), revs.Running.DeployedAt.UTC())
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		params string
		max    int
		setup  func(*fakeRun)
		want   string
	}{
		{
			name: "a service that does not exist", setup: func(f *fakeRun) { f.missing = true },
			want: `service "checkout" does not exist in project "shop-prod", location "europe-west1"`,
		},
		{
			name: "a service that is applying a change", setup: func(f *fakeRun) { f.reconciling = true },
			want: "the service is still applying an earlier change; propose again when it has finished",
		},
		{
			name: "traffic split between two revisions",
			setup: func(f *fakeRun) {
				f.traffic = []map[string]any{
					{"type": allocationRevision, "revision": "checkout-00008-bbb", "percent": 60},
					{"type": allocationRevision, "revision": "checkout-00007-aaa", "percent": 40},
				}
			},
			want: "traffic is split between several revisions (checkout-00008-bbb 60 percent, checkout-00007-aaa 40 percent); this action rolls back only a service that sends 100 percent of traffic to one revision",
		},
		{
			name: "traffic split with the latest revision",
			setup: func(f *fakeRun) {
				f.traffic = []map[string]any{
					{"type": allocationLatest, "percent": 90},
					{"type": allocationRevision, "revision": "checkout-00007-aaa", "percent": 10},
				}
			},
			want: "traffic is split between several revisions (the latest ready revision 90 percent, checkout-00007-aaa 10 percent)",
		},
		{
			name: "a tagged entry with no traffic next to the one that serves",
			setup: func(f *fakeRun) {
				f.traffic = []map[string]any{
					{"type": allocationLatest, "percent": 100},
					{"type": allocationRevision, "revision": "checkout-00007-aaa", "percent": 0, "tag": "candidate"},
				}
			},
			want: "the traffic list has more than one entry, or an entry with no traffic or a tag; setting the traffic would drop it, so this action does not change this service",
		},
		{
			name: "one entry with a tag",
			setup: func(f *fakeRun) {
				f.traffic = []map[string]any{{"type": allocationLatest, "percent": 100, "tag": "blue"}}
			},
			want: "the traffic list has more than one entry, or an entry with no traffic or a tag",
		},
		{
			name:  "one entry that does not carry all the traffic",
			setup: func(f *fakeRun) { f.traffic = []map[string]any{{"type": allocationLatest, "percent": 50}} },
			want:  "the traffic list has more than one entry, or an entry with no traffic or a tag",
		},
		{
			name: "no ready revision to roll back from", setup: func(f *fakeRun) { f.latestReady = "" },
			want: "the service has no ready revision to roll back from",
		},
		{
			name:  "the running revision is not in the list",
			setup: func(f *fakeRun) { f.latestReady = "checkout-00099-nnn" },
			want:  `revision "checkout-00099-nnn" is not in the list of revisions of the service`,
		},
		{
			name: "nothing older than the running revision", setup: func(f *fakeRun) { f.traffic = pinnedTraffic("checkout-00004-xxx") },
			want: "the service has no earlier ready revision to go back to",
		},
		{
			name: "fewer earlier ready revisions than asked", max: 5, params: `{"service":"checkout","revisionsBack":4}`,
			want: "the service has only 3 earlier ready revisions; 4 back is not possible",
		},
		{
			name: "a list that is not in creation order",
			setup: func(f *fakeRun) {
				f.revisions[2]["createTime"] = "2026-11-04T08:00:00Z"
			},
			want: "the revision to go back to is not older than the running one, so the list is not in creation order",
		},
		{
			name: "more revisions than the action reads",
			setup: func(f *fakeRun) {
				f.pages = 100
				f.revisions = nil

				for i := range maxRevisions + 50 {
					f.revisions = append(f.revisions, fakeRevision(fmt.Sprintf("checkout-r%d", i), "2026-11-03T08:00:00Z", true, "", "x"))
				}

				f.latestReady = "checkout-r" + fmt.Sprint(maxRevisions+10)
			},
			want: fmt.Sprintf("the service has more than %d revisions; this action does not read that far", maxRevisions),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			run := newFakeRun(t, api)

			if tt.setup != nil {
				run.update(tt.setup)
			}

			cfg := limitedConfig()
			if tt.max > 0 {
				cfg.MaxRevisionsBack = new(tt.max)
			}

			params := tt.params
			if params == "" {
				params = paramsJSON
			}

			_, err := buildAction(t, api, cfg).Snapshot(context.Background(), json.RawMessage(params))
			requireRefusal(t, err, tt.want)
			require.True(t, onlyGets(api))
		})
	}
}

func TestSnapshotOutsideTheLimits(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	_, err := buildAction(t, api, limitedConfig()).Snapshot(context.Background(), json.RawMessage(`{"service":"checkout","revisionsBack":2}`))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, api.Requests(), "nothing is sent for a request outside the limits")
}

func TestSnapshotReadsEveryPage(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	run := newFakeRun(t, api)
	run.update(func(f *fakeRun) { f.pages = 2 })

	cfg := limitedConfig()
	cfg.MaxRevisionsBack = new(3)

	snap := snapshotOf(t, buildAction(t, api, cfg), `{"service":"checkout","revisionsBack":3}`)

	revs, err := snap.Revisions()
	require.NoError(t, err)
	require.Equal(t, "checkout-00004-xxx", revs.GoesBackTo.ID)

	pages := 0

	for _, r := range api.Requests() {
		if r.Path == revisionsPath {
			pages++
		}
	}

	require.Equal(t, 3, pages, "six revisions in pages of two")
}

func TestSnapshotErrorsThatAreNotRefusals(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	api.Handle(http.MethodGet, servicePath, fakehttp.Response{Status: http.StatusForbidden, Body: []byte(`{"error":{"code":403,"status":"PERMISSION_DENIED","message":"no run.services.get"}}`)})

	_, err := buildAction(t, api, limitedConfig()).Snapshot(context.Background(), json.RawMessage(paramsJSON))
	require.ErrorContains(t, err, "Google Cloud answered 403 PERMISSION_DENIED: no run.services.get")
	require.NotErrorIs(t, err, action.ErrLimit)
}

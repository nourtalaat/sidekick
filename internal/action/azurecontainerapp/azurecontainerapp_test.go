package azurecontainerapp

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

const otherSub = "99999999-8888-7777-6666-555555555555"

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  string
		wantErr error
		wantMsg string
	}{
		{name: "one revision back", params: string(params(1))},
		{name: "as many revisions back as maxRevisionsBack allows", params: string(params(2))},
		{name: "one more than maxRevisionsBack allows", params: string(params(3)), wantErr: action.ErrLimit, wantMsg: "3 revisions back is more than maxRevisionsBack allows (2)"},
		{name: "no revisions back", params: string(params(0)), wantErr: action.ErrParams},
		{name: "a negative number of revisions back", params: string(params(-1)), wantErr: action.ErrParams},
		{name: "revisionsBack left out", params: `{"app": "checkout"}`},
		{name: "only the app", params: `{"app": "checkout-api"}`},
		{name: "an app in capitals", params: `{"app": "CHECKOUT"}`},
		{name: "no app", params: `{}`, wantErr: action.ErrLimit, wantMsg: `app "" is not one of limits.apps`},
		{name: "an app outside limits.apps", params: `{"app": "payments"}`, wantErr: action.ErrLimit, wantMsg: `app "payments" is not one of limits.apps`},
		{name: "a subscription outside the limits", params: `{"app": "checkout", "subscription": "` + otherSub + `"}`, wantErr: action.ErrLimit, wantMsg: "is not one of limits.subscriptions"},
		{name: "a resource group outside the limits", params: `{"app": "checkout", "resourceGroup": "billing"}`, wantErr: action.ErrLimit, wantMsg: `resourceGroup "billing" is not one of limits.resourceGroups`},
		{name: "a resource group in capitals", params: `{"app": "checkout", "resourceGroup": "SHOP"}`},
		{name: "a key that is not a parameter", params: `{"app": "checkout", "force": true}`, wantErr: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			r := newTestAction(t, newFakeARM(t), 2)

			err := r.Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorContains(t, err, tt.wantMsg)
		})
	}
}

func TestValidateWithSeveralAllowed(t *testing.T) {
	t.Parallel()

	cfg := validConfig()
	cfg.Limits.Subscriptions = []string{testSub, otherSub}
	cfg.Limits.ResourceGroups = []string{testGroup, "billing"}

	r := newRollback("rollback", cfg, fakeCredential{}, nil)

	require.NoError(t, r.Validate(t.Context(), json.RawMessage(`{"app": "checkout-api", "subscription": "`+otherSub+`", "resourceGroup": "billing"}`)))
	require.ErrorIs(t, r.Validate(t.Context(), json.RawMessage(`{"app": "checkout-api", "resourceGroup": "billing"}`)), action.ErrParams)
	require.ErrorIs(t, r.Validate(t.Context(), json.RawMessage(`{"app": "checkout-api", "subscription": "`+testSub+`"}`)), action.ErrParams)
}

func TestSpec(t *testing.T) {
	t.Parallel()

	r := newTestAction(t, newFakeARM(t), 2)
	spec := r.Spec()

	require.Equal(t, "rollback", spec.Name)
	require.Contains(t, spec.Description, "at most 2 revisions")
	require.Contains(t, spec.Description, testSub)
	require.Contains(t, spec.Description, "checkout-*")
	require.True(t, json.Valid(spec.ParamsSchema))
}

func TestNew(t *testing.T) {
	t.Parallel()

	secret := registry.SecretRef{Name: "azure-rollback", Key: "client-secret"}

	cfg := validConfig()
	cfg.TenantID, cfg.ClientID, cfg.ClientSecret = testTenant, testClient, &secret

	_, err := New(t.Context(), "rollback", cfg, registry.Deps{})
	require.ErrorIs(t, err, errNoSecrets)

	built, err := New(t.Context(), "rollback", validConfig(), registry.Deps{})
	require.NoError(t, err)
	require.Implements(t, (*action.Snapshotter)(nil), built)
	require.Implements(t, (*action.Undoer)(nil), built)
	require.NotImplements(t, (*action.MonitorFixer)(nil), built)
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(f *fakeARM)
		back        int
		wantRunning string
		wantBack    string
		wantVersion string
		wantNotes   [2]string
	}{
		{name: "one back", back: 1, wantRunning: "checkout--r3", wantBack: "checkout--r2", wantVersion: `^pinned [0-9a-f]{32}$`, wantNotes: [2]string{notesByName, notesMoves}},
		{name: "two back", back: 2, wantRunning: "checkout--r3", wantBack: "checkout--r1", wantVersion: `^pinned `, wantNotes: [2]string{notesByName, notesMoves}, setup: func(f *fakeARM) {
			f.revisions[0].active = true
		}},
		{name: "traffic follows the latest revision", back: 1, wantRunning: "checkout--r3", wantBack: "checkout--r2", wantVersion: `^latest [0-9a-f]{32}$`, wantNotes: [2]string{notesFollowsLatest, notesPinned}, setup: func(f *fakeARM) {
			f.traffic = []map[string]any{{"latestRevision": true, "weight": 100}}
			f.latestReady = "checkout--r3"
		}},
		{name: "a revision made after the running one is not counted", back: 1, wantRunning: "checkout--r2", wantBack: "checkout--r1", wantVersion: `^pinned `, setup: func(f *fakeARM) {
			f.traffic = []map[string]any{{"revisionName": "checkout--r2", "weight": 100}}
			f.revisions[0].active = true
		}, wantNotes: [2]string{notesByName, notesMoves}},
		{name: "a weight 0 entry with a label is no running revision", back: 1, wantRunning: "checkout--r3", wantBack: "checkout--r2", wantVersion: `^pinned `, setup: func(f *fakeARM) {
			f.traffic = []map[string]any{{"revisionName": "checkout--r3", "weight": 100}, {"revisionName": "checkout--r4", "weight": 0, "label": "staging"}}
		}, wantNotes: [2]string{notesByName, notesMoves}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeARM(t)
			if tt.setup != nil {
				f.change(tt.setup)
			}

			r := newTestAction(t, f, 2)
			snap := snapshotOf(t, r, tt.back)

			require.True(t, snap.Revision)
			require.Equal(t, "azure:containerapp/"+testSub+"/shop/checkout", snap.Target)
			require.Regexp(t, tt.wantVersion, snap.Version)

			revs, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, tt.wantRunning, revs.Running.ID)
			require.Equal(t, tt.wantBack, revs.GoesBackTo.ID)
			require.Equal(t, revisionKind, revs.Running.Kind)
			require.Empty(t, revs.Running.DeployedBy, "Container Apps does not record who deployed")
			require.Empty(t, revs.GoesBackTo.DeployedBy)

			if tt.wantNotes != ([2]string{}) {
				require.Equal(t, tt.wantNotes[0], revs.Running.Notes)
				require.Equal(t, tt.wantNotes[1], revs.GoesBackTo.Notes)
			}

			require.Empty(t, f.patchBodies(), "a snapshot changes nothing")
		})
	}
}

func TestSnapshotFacts(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	r := newTestAction(t, f, 1)
	snap := snapshotOf(t, r, 1)

	revs, err := snap.Revisions()
	require.NoError(t, err)

	require.Equal(t, "shop.azurecr.io/checkout:1.4.3", revs.Running.Version)
	require.Equal(t, "shop.azurecr.io/checkout:1.4.2", revs.GoesBackTo.Version)
	require.NotNil(t, revs.Running.DeployedAt)
	require.True(t, revs.Running.DeployedAt.Equal(testStart.Add(2*time.Hour)))
	require.True(t, revs.GoesBackTo.DeployedAt.Equal(testStart.Add(time.Hour)))

	for _, req := range f.api.Requests() {
		require.Equal(t, "Bearer "+testToken, req.Header.Get("Authorization"))
		require.Equal(t, "2026-07-01", req.Query.Get("api-version"))
		require.Equal(t, http.MethodGet, req.Method)
	}

	require.Len(t, f.api.Requests(), 2, "one read of the app and one of its revisions")
}

func TestSnapshotRefuses(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(f *fakeARM)
		max   int
		back  int
		want  string
	}{
		{
			name: "single revision mode", max: 1, back: 1,
			setup: func(f *fakeARM) { f.mode = "Single" },
			want: "container app checkout is in single revision mode: Azure sends all of its traffic to the latest revision, " +
				"so there is no traffic to move. To go back, deploy the earlier template through your own pipeline; this action does not do that.",
		},
		{
			name: "no revisions mode, which is single", max: 1, back: 1,
			setup: func(f *fakeARM) { f.mode = "" },
			want:  "container app checkout is in single revision mode",
		},
		{
			name: "no ingress", max: 1, back: 1,
			setup: func(f *fakeARM) { f.ingress = false },
			want:  "container app checkout has no ingress, so it has no traffic weights to move.",
		},
		{
			name: "traffic split between revisions", max: 1, back: 1,
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"revisionName": "checkout--r3", "weight": 60}, {"revisionName": "checkout--r4", "weight": 40}}
			},
			want: "traffic of container app checkout is split between several revisions (checkout--r3 60, checkout--r4 40). " +
				"This action moves all of the traffic from one revision to another and does not change a split.",
		},
		{
			name: "no traffic to any revision", max: 1, back: 1,
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"revisionName": "checkout--r3", "weight": 0, "label": "x"}}
			},
			want: "container app checkout sends no traffic to any revision, so there is no running revision to go back from.",
		},
		{
			name: "traffic to a revision that is not listed", max: 1, back: 1,
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"revisionName": "checkout--gone", "weight": 100}}
			},
			want: "container app checkout sends its traffic to revision checkout--gone, which Azure does not list among its revisions.",
		},
		{
			name: "traffic to a latest revision that is not named", max: 1, back: 1,
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"latestRevision": true, "weight": 100}}
				f.latestReady = ""
			},
			want: "container app checkout sends traffic to its latest revision, but Azure does not name a latest revision that is ready.",
		},
		{
			name: "the revisions list reports other weights", max: 1, back: 1,
			setup: func(f *fakeARM) { f.weightOverride = map[string]int{"checkout--r3": 90, "checkout--r4": 10} },
			want: "Azure reports a different traffic split for container app checkout than its ingress settings show. " +
				"Try again when a deployment has finished.",
		},
		{
			name: "further back than there are revisions", max: 3, back: 3,
			want: "container app checkout has only 2 revisions older than checkout--r3, so it cannot go 3 back.",
		},
		{
			name: "a revision with no created time", max: 2, back: 2,
			setup: func(f *fakeARM) { f.revisions[0].created = nil; f.revisions[0].active = true },
			want:  "revision checkout--r1 has no created time, so its place in the order of revisions is not known.",
		},
		{
			name: "an inactive revision one back", max: 1, back: 1,
			setup: func(f *fakeARM) { f.revisions[1].active = false },
			want: "revision checkout--r2, 1 back from checkout--r3, is inactive. Azure sends no traffic to an inactive revision, " +
				"and this action does not activate revisions. Activate it first, or ask for a different number of revisions back.",
		},
		{
			name: "an inactive revision two back", max: 2, back: 2,
			want: "revision checkout--r1, 2 back from checkout--r3, is inactive.",
		},
		{
			name: "an app that does not exist", max: 1, back: 1,
			setup: func(f *fakeARM) { f.getStatus = http.StatusNotFound },
			want:  "container app checkout does not exist in resource group shop of subscription " + testSub + ".",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeARM(t)
			if tt.setup != nil {
				f.change(tt.setup)
			}

			r := newTestAction(t, f, tt.max)

			_, err := r.Snapshot(t.Context(), params(tt.back))
			require.ErrorIs(t, err, action.ErrLimit, "a refusal is shown to the model as one")
			require.ErrorContains(t, err, tt.want)
			require.Empty(t, f.patchBodies())
		})
	}
}

func TestSnapshotLimitIsCheckedBeforeAnyCall(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	r := newTestAction(t, f, 1)

	_, err := r.Snapshot(t.Context(), params(2))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, f.api.Requests())
}

func TestSnapshotReadFailure(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	f.change(func(f *fakeARM) { f.getStatus = http.StatusForbidden })

	r := newTestAction(t, f, 1)

	_, err := r.Snapshot(t.Context(), params(1))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrLimit, "it is a failure to read, not a refusal")
	require.ErrorContains(t, err, "Azure answered 403 Failure403")
}

func TestExecuteMovesTraffic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(f *fakeARM)
		wantTraffic string
		wantSummary string
		notSummary  string
	}{
		{
			name:        "traffic by name",
			wantTraffic: `[{"revisionName":"checkout--r2","weight":100}]`,
			wantSummary: "Moved all traffic of container app checkout from revision checkout--r3 to revision checkout--r2.",
			notSummary:  "pinned",
		},
		{
			name: "traffic that follows the latest revision",
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"latestRevision": true, "weight": 100}}
				f.latestReady = "checkout--r3"
			},
			wantTraffic: `[{"revisionName":"checkout--r2","weight":100}]`,
			wantSummary: "Traffic is now pinned to that revision by name, so a new revision gets no traffic until someone moves it.",
		},
		{
			name: "a label stays",
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"revisionName": "checkout--r3", "weight": 100}, {"revisionName": "checkout--r4", "weight": 0, "label": "staging"}}
			},
			wantTraffic: `[{"label":"staging","revisionName":"checkout--r4","weight":0},{"revisionName":"checkout--r2","weight":100}]`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeARM(t)
			if tt.setup != nil {
				f.change(tt.setup)
			}

			r := newTestAction(t, f, 1)
			snap := snapshotOf(t, r, 1)

			res, err := r.Execute(t.Context(), invocation(1, snap))
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.wantSummary)

			if tt.notSummary != "" {
				require.NotContains(t, res.Summary, tt.notSummary)
			}

			require.Len(t, res.Links, 1)
			require.Contains(t, res.Links[0].URL, "/containerApps/checkout")

			sent := trafficJSON(t, f.patchBodies())
			require.Len(t, sent, 1)
			require.JSONEq(t, tt.wantTraffic, sent[0])

			for _, req := range f.api.Requests() {
				require.Equal(t, "Bearer "+testToken, req.Header.Get("Authorization"))

				if req.Method == http.MethodPatch {
					require.Equal(t, "2026-07-01", req.Query.Get("api-version"))
					require.Equal(t, appPath, req.Path)
				}
			}
		})
	}
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(f *fakeARM)
	}{
		{name: "a deploy moved the traffic", change: func(f *fakeARM) { f.traffic = []map[string]any{{"revisionName": "checkout--r4", "weight": 100}} }},
		{name: "the traffic was split", change: func(f *fakeARM) {
			f.traffic = []map[string]any{{"revisionName": "checkout--r3", "weight": 50}, {"revisionName": "checkout--r4", "weight": 50}}
		}},
		{name: "a label was added", change: func(f *fakeARM) {
			f.traffic = []map[string]any{{"revisionName": "checkout--r3", "weight": 100}, {"revisionName": "checkout--r4", "weight": 0, "label": "x"}}
		}},
		{name: "a new revision became the latest", change: func(f *fakeARM) { f.latest, f.latestReady = "checkout--r5", "checkout--r5" }},
		{name: "the app went to single revision mode", change: func(f *fakeARM) { f.mode = "Single" }},
		{name: "the ingress was removed", change: func(f *fakeARM) { f.ingress = false }},
		{name: "the revision to go back to was deactivated", change: func(f *fakeARM) { f.revisions[1].active = false }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeARM(t)
			r := newTestAction(t, f, 1)
			snap := snapshotOf(t, r, 1)

			f.change(tt.change)

			_, err := r.Execute(t.Context(), invocation(1, snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.Empty(t, f.patchBodies(), "nothing is changed")
		})
	}
}

func TestExecuteRepeatedChangesOnce(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	r := newTestAction(t, f, 1)
	inv := invocation(1, snapshotOf(t, r, 1))

	first, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, first.Summary, second.Summary)
	require.Len(t, f.patchBodies(), 1)
}

func TestExecuteAfterACrash(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	r := newTestAction(t, f, 1)
	inv := invocation(1, snapshotOf(t, r, 1))

	f.change(func(f *fakeARM) { f.operationStatus = http.StatusInternalServerError })

	_, err := r.Execute(t.Context(), inv)
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent, "the job tries again")
	require.NotErrorIs(t, err, action.ErrConflict)
	require.Len(t, f.patchBodies(), 1, "the change was made before the wait failed")

	_, err = r.Execute(t.Context(), inv)
	require.ErrorIs(t, err, errInProgress, "the next try waits while Azure updates the app")
	require.Len(t, f.patchBodies(), 1)

	f.change(func(f *fakeARM) { f.operationStatus, f.state = 0, "Succeeded" })

	res, err := r.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "to revision checkout--r2")
	require.Len(t, f.patchBodies(), 1, "the change is not made twice")
}

func TestExecuteWhenTheChangeWasNotKept(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	r := newTestAction(t, f, 1)
	inv := invocation(1, snapshotOf(t, r, 1))

	f.change(func(f *fakeARM) { f.state = "Failed" })

	_, err := r.Execute(t.Context(), inv)
	require.NoError(t, err, "a failed earlier update is not a reason to wait")
	require.Len(t, f.patchBodies(), 1)
}

func TestExecuteAzureErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setup         func(f *fakeARM)
		wantPermanent bool
	}{
		{name: "the permission is refused", setup: func(f *fakeARM) { f.patchStatus = http.StatusForbidden }, wantPermanent: true},
		{name: "the body is refused", setup: func(f *fakeARM) { f.patchStatus = http.StatusBadRequest }, wantPermanent: true},
		{name: "another update is running", setup: func(f *fakeARM) { f.patchStatus = http.StatusConflict }},
		{name: "Azure is down", setup: func(f *fakeARM) { f.patchStatus = http.StatusServiceUnavailable }},
		{name: "the app was deleted", setup: func(f *fakeARM) { f.getStatus = http.StatusNotFound }, wantPermanent: true},
		{name: "the app cannot be read", setup: func(f *fakeARM) { f.getStatus = http.StatusTooManyRequests }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeARM(t)
			r := newTestAction(t, f, 1)
			inv := invocation(1, snapshotOf(t, r, 1))

			f.change(tt.setup)

			_, err := r.Execute(t.Context(), inv)
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
		})
	}
}

func TestExecuteChecksItsInput(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	r := newTestAction(t, f, 1)
	snap := snapshotOf(t, r, 1)

	other := snap
	other.Target = "azure:containerapp/" + testSub + "/shop/payments"

	notRollback := action.Snapshot{Target: snap.Target, Before: json.RawMessage(`{}`), After: json.RawMessage(`{}`)}

	tests := []struct {
		name string
		inv  action.Invocation
		want error
	}{
		{name: "no snapshot", inv: action.Invocation{Params: params(1)}, want: action.ErrPermanent},
		{name: "the snapshot of another app", inv: invocation(1, other), want: action.ErrPermanent},
		{name: "a snapshot that is not a rollback", inv: invocation(1, notRollback), want: action.ErrPermanent},
		{name: "more revisions back than the limit", inv: invocation(2, snap), want: action.ErrLimit},
		{name: "an app outside the limits", inv: action.Invocation{Params: json.RawMessage(`{"app": "payments"}`), Snapshot: &snap}, want: action.ErrLimit},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := r.Execute(t.Context(), tt.inv)
			require.ErrorIs(t, err, tt.want)
		})
	}

	t.Cleanup(func() { require.Len(t, f.api.Requests(), 2, "only the snapshot read anything") })
}

// rollBack runs a rollback one revision back and returns the snapshot.
func rollBack(t *testing.T, r *Rollback) action.Snapshot {
	t.Helper()

	snap := snapshotOf(t, r, 1)

	_, err := r.Execute(t.Context(), invocation(1, snap))
	require.NoError(t, err)

	return snap
}

// undo runs Undo of the rollback that snap describes.
func undo(t *testing.T, r *Rollback, snap action.Snapshot, force bool) (action.Result, error) {
	t.Helper()

	return r.Undo(t.Context(), invocation(1, snap), snap, force)
}

func TestUndoRestoresTheTraffic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(f *fakeARM)
		beforeUndo  func(f *fakeARM)
		wantTraffic string
		wantSummary string
		notSummary  string
	}{
		{
			name:        "by name",
			wantTraffic: `[{"revisionName":"checkout--r3","weight":100}]`,
			wantSummary: "Moved all traffic of container app checkout back to revision checkout--r3.",
			notSummary:  "latest revision again",
		},
		{
			name: "to follow the latest revision again",
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"latestRevision": true, "weight": 100}}
				f.latestReady = "checkout--r3"
			},
			wantTraffic: `[{"latestRevision":true,"weight":100}]`,
			wantSummary: "Traffic follows the latest revision again.",
		},
		{
			name: "by name when a newer revision is the latest by now",
			setup: func(f *fakeARM) {
				f.traffic = []map[string]any{{"latestRevision": true, "weight": 100}}
				f.latestReady = "checkout--r3"
			},
			beforeUndo:  func(f *fakeARM) { f.latestReady = "checkout--r4" },
			wantTraffic: `[{"revisionName":"checkout--r3","weight":100}]`,
			notSummary:  "latest revision again",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeARM(t)
			if tt.setup != nil {
				f.change(tt.setup)
			}

			r := newTestAction(t, f, 1)
			snap := rollBack(t, r)

			if tt.beforeUndo != nil {
				f.change(tt.beforeUndo)
			}

			res, err := undo(t, r, snap, false)
			require.NoError(t, err)
			require.Contains(t, res.Summary, tt.wantSummary)

			if tt.notSummary != "" {
				require.NotContains(t, res.Summary, tt.notSummary)
			}

			sent := trafficJSON(t, f.patchBodies())
			require.Len(t, sent, 2)
			require.JSONEq(t, tt.wantTraffic, sent[1])

			_, err = undo(t, r, snap, false)
			require.NoError(t, err, "an undo that is repeated succeeds")
			require.Len(t, f.patchBodies(), 2, "and changes nothing")
		})
	}
}

func TestUndoConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		change   func(f *fakeARM)
		wantID   string
		wantNote string
	}{
		{name: "a deploy moved the traffic", change: func(f *fakeARM) { f.traffic = []map[string]any{{"revisionName": "checkout--r4", "weight": 100}} }, wantID: "checkout--r4"},
		{
			name: "the traffic was split",
			change: func(f *fakeARM) {
				f.traffic = []map[string]any{{"revisionName": "checkout--r2", "weight": 70}, {"revisionName": "checkout--r4", "weight": 30}}
			},
			wantID: "none, traffic is not on one revision", wantNote: "checkout--r2 70, checkout--r4 30",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeARM(t)
			r := newTestAction(t, f, 1)
			snap := rollBack(t, r)

			f.change(tt.change)

			_, err := undo(t, r, snap, false)
			require.ErrorIs(t, err, action.ErrConflict)

			var conflict *action.ConflictError

			require.ErrorAs(t, err, &conflict)

			var live struct {
				Kind  string `json:"kind"`
				ID    string `json:"id"`
				Notes string `json:"notes"`
			}

			require.NoError(t, json.Unmarshal(conflict.Live, &live))
			require.Equal(t, tt.wantID, live.ID)
			require.Equal(t, revisionKind, live.Kind)

			if tt.wantNote != "" {
				require.Equal(t, tt.wantNote, live.Notes)
			}

			require.Len(t, f.patchBodies(), 1, "nothing is restored without force")

			_, err = undo(t, r, snap, true)
			require.NoError(t, err, "force restores it")

			sent := trafficJSON(t, f.patchBodies())
			require.Len(t, sent, 2)
			require.JSONEq(t, `[{"revisionName":"checkout--r3","weight":100}]`, sent[1])
		})
	}
}

func TestUndoRefuses(t *testing.T) {
	t.Parallel()

	f := newFakeARM(t)
	r := newTestAction(t, f, 1)
	snap := rollBack(t, r)

	noMode := snap
	noMode.Version = "abc"

	_, err := undo(t, r, noMode, false)
	require.ErrorIs(t, err, action.ErrPermanent)

	f.change(func(f *fakeARM) { f.revisions[2].active = false })

	_, err = undo(t, r, snap, false)
	require.ErrorIs(t, err, action.ErrPermanent, "the revision to restore is inactive now")
	require.ErrorIs(t, err, errNotActive)

	f.change(func(f *fakeARM) { f.revisions[2].active = true; f.state = "InProgress" })

	_, err = undo(t, r, snap, false)
	require.ErrorIs(t, err, errInProgress)
	require.NotErrorIs(t, err, action.ErrPermanent)
	require.Len(t, f.patchBodies(), 1)
}

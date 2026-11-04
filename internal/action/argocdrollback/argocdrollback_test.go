package argocdrollback

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/actiontest"
	"github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		params  string
		wantErr string
		wantIs  error
	}{
		{name: "one back by default", params: oneBack},
		{name: "one back named", params: `{"application": "shop/checkout", "revisionsBack": 1}`},
		{name: "two back with a limit of two", change: allowBack(2), params: twoBack},
		{name: "an application of the project", params: `{"application": "shop/cart"}`},
		{name: "two back over the default limit", params: twoBack, wantErr: "2 revisions back is more than maxRevisionsBack allows (1)", wantIs: action.ErrLimit},
		{name: "three back with a limit of two", change: allowBack(2), params: `{"application": "shop/checkout", "revisionsBack": 3}`, wantErr: "3 revisions back is more than maxRevisionsBack allows (2)", wantIs: action.ErrLimit},
		{name: "zero back", params: `{"application": "shop/checkout", "revisionsBack": 0}`, wantErr: "0 revisions back is not a number of revisions", wantIs: action.ErrParams},
		{name: "negative", params: `{"application": "shop/checkout", "revisionsBack": -1}`, wantIs: action.ErrParams},
		{name: "an application of another project", params: `{"application": "platform/ingress"}`, wantErr: `application "platform/ingress" is not one the action may roll back; allowed: shop/*`, wantIs: action.ErrLimit},
		{name: "a name without its project", params: `{"application": "checkout"}`, wantErr: "must have the form project/name", wantIs: action.ErrParams},
		{name: "a name with a slash too many", params: `{"application": "shop/a/b"}`, wantIs: action.ErrParams},
		{name: "no application", params: `{}`, wantIs: action.ErrParams},
		{name: "an extra parameter", params: `{"application": "shop/checkout", "prune": true}`, wantErr: `unknown field "prune"`, wantIs: action.ErrParams},
		{name: "text that is not JSON", params: `roll it back`, wantIs: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			a := newTestAction(t, api, tt.change)

			err := a.Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantIs == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, tt.wantIs)
			}

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
			}

			require.Empty(t, api.Requests(), "Validate sends nothing")
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	a := newTestAction(t, fakehttp.New(t), allowBack(3))
	spec := a.Spec()

	require.Equal(t, "argocd-rollback", spec.Name)
	require.Contains(t, spec.Description, "Allowed applications: shop/*.")
	require.Contains(t, spec.Description, "Up to 3 revisions back")

	var schema struct {
		Required   []string                  `json:"required"`
		Properties map[string]map[string]any `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(spec.ParamsSchema, &schema))
	require.Equal(t, []string{"application"}, schema.Required)
	require.InDelta(t, 3, schema.Properties["revisionsBack"]["maximum"], 0)
}

func TestSpecSnapshot(t *testing.T) {
	t.Parallel()

	rollback := newTestAction(t, fakehttp.New(t), func(c *Config) {
		c.Limits = Limits{Applications: argocd.Allowlist{"storefront/*"}}
	})

	actiontest.CheckSpecSnapshot(t, Type, rollback)
}

func TestNewIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(c *Config)
		want   bool
	}{
		{name: "without the opt-in", want: true},
		{name: "with the opt-in", change: func(c *Config) { c.AllowReadWrite = true }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := newTestAction(t, fakehttp.New(t), tt.change)

			require.Equal(t, tt.want, action.ReadWriteBlocked(a), "the built action reports it")
		})
	}
}

func TestNewMakesNoCallAndNeedsSecrets(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)
	newTestAction(t, api, nil)
	require.Empty(t, api.Requests())

	_, err := New(t.Context(), "x", validConfig(), registry.Deps{})
	require.Error(t, err)
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		change      func(c *Config)
		params      string
		wantID      string
		wantVersion string
	}{
		{name: "one back", params: oneBack, wantID: "6", wantVersion: "7:" + revC},
		{name: "two back", change: allowBack(2), params: twoBack, wantID: "5", wantVersion: "7:" + revC},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, _ := newFakeArgo(t)
			a := newTestAction(t, api, tt.change)

			snap := snapshotOf(t, a, tt.params)

			require.True(t, snap.Revision)
			require.Equal(t, "argocd:application/shop/checkout", snap.Target)
			require.Equal(t, tt.wantVersion, snap.Version)

			revs, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, "Argo CD deployment", revs.Running.Kind)
			require.Equal(t, "7", revs.Running.ID)
			require.Equal(t, "cccc333", revs.Running.Version)
			require.Equal(t, "ana", revs.Running.DeployedBy)
			require.Equal(t, "Images in the cluster now: registry.example.com/shop/checkout:1.4", revs.Running.Notes)
			require.NotNil(t, revs.Running.DeployedAt)
			require.Equal(t, tt.wantID, revs.GoesBackTo.ID)
			require.Equal(t, "Argo CD deployment", revs.GoesBackTo.Kind)
			require.Empty(t, revs.GoesBackTo.Notes, "Argo CD keeps no images of an older entry")
			require.Equal(t, "ana", revs.GoesBackTo.DeployedBy)

			for _, r := range api.Requests() {
				require.Equal(t, http.MethodGet, r.Method, "Snapshot only reads")
			}
		})
	}
}

func TestSnapshotRevisionsWhenTheStarterIsNotRecorded(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	f.update(func(f *fakeArgo) {
		for _, e := range f.history {
			delete(e, "initiatedBy")
			delete(e, "deployedAt")
		}
	})

	a := newTestAction(t, api, nil)
	revs, err := snapshotOf(t, a, oneBack).Revisions()
	require.NoError(t, err)

	require.Empty(t, revs.GoesBackTo.DeployedBy)
	require.Nil(t, revs.GoesBackTo.DeployedAt)
	require.Empty(t, revs.Running.DeployedBy)
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(f *fakeArgo)
		params  string
		failGet *fakeFailure
		wantErr string
	}{
		{
			name:    "auto-sync is on",
			change:  func(f *fakeArgo) { f.autoSync = true },
			params:  oneBack,
			wantErr: "Argo CD would sync the application back, and refuses a rollback while auto-sync is on. Propose a revert pull request with `github.revert` or `gitlab.revert` instead.",
		},
		{
			name:    "an operation runs",
			change:  func(f *fakeArgo) { f.operation, f.polls, f.pollsToEnd = revB, 1, -1 },
			params:  oneBack,
			wantErr: "An operation is running on application shop/checkout (phase Running). Wait for it to finish, then propose again.",
		},
		{
			name:    "several sources",
			change:  func(f *fakeArgo) { f.sources = 2 },
			params:  oneBack,
			wantErr: "Application shop/checkout has several sources, and this action rolls back applications with one source only. Propose a revert pull request, or roll it back in Argo CD.",
		},
		{
			name:    "one source listed under sources",
			change:  func(f *fakeArgo) { f.sources = 1 },
			params:  oneBack,
			wantErr: "Application shop/checkout has several sources",
		},
		{
			name:    "the source hydrator",
			change:  func(f *fakeArgo) { f.hydrator = true },
			params:  oneBack,
			wantErr: "Application shop/checkout uses the source hydrator, and this action does not roll those back. Propose a revert pull request, or roll it back in Argo CD.",
		},
		{
			name:    "no history",
			change:  func(f *fakeArgo) { f.history = nil },
			params:  oneBack,
			wantErr: "Application shop/checkout has no deployment history, so there is nothing to go back to.",
		},
		{
			name:    "the history is shorter than the request",
			change:  func(f *fakeArgo) { f.history = f.history[1:] },
			params:  twoBack,
			wantErr: "Application shop/checkout has 1 earlier revision in its history that differs from the one running now, so it cannot go back 2. Argo CD keeps only the last 10 deployments (revisionHistoryLimit), and older ones are gone.",
		},
		{
			name: "the history limit is in the message",
			change: func(f *fakeArgo) {
				limit := 3
				f.limit = &limit
				f.history = f.history[2:]
			},
			params:  oneBack,
			wantErr: "Argo CD keeps only the last 3 deployments (revisionHistoryLimit)",
		},
		{
			name:    "Argo CD has no such application",
			params:  oneBack,
			failGet: &fakeFailure{status: http.StatusNotFound, message: "applications.argoproj.io \"checkout\" not found"},
			wantErr: "Argo CD has no application shop/checkout, or the token may not get it.",
		},
		{
			name:    "the token may not get it",
			params:  oneBack,
			failGet: &fakeFailure{status: http.StatusForbidden, message: "permission denied"},
			wantErr: "Argo CD has no application shop/checkout, or the token may not get it.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeArgo(t)
			f.update(func(f *fakeArgo) {
				if tt.change != nil {
					tt.change(f)
				}

				f.getFailure = tt.failGet
			})

			a := newTestAction(t, api, allowBack(2))

			_, err := a.Snapshot(t.Context(), json.RawMessage(tt.params))
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
			require.Empty(t, f.rolledBack(), "Snapshot changes nothing")
		})
	}
}

func TestSnapshotErrorsThatPass(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	f.update(func(f *fakeArgo) { f.getFailure = &fakeFailure{status: http.StatusBadGateway, message: "bad gateway"} })

	a := newTestAction(t, api, nil)

	_, err := a.Snapshot(t.Context(), json.RawMessage(oneBack))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrLimit)
	require.NotErrorIs(t, err, action.ErrPermanent)
}

func TestSnapshotRefusesAnApplicationOutsideTheLimits(t *testing.T) {
	t.Parallel()

	api, _ := newFakeArgo(t)
	a := newTestAction(t, api, nil)

	_, err := a.Snapshot(t.Context(), json.RawMessage(`{"application": "platform/checkout"}`))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, api.Requests(), "nothing is sent for an application the config does not allow")
}

func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		change       func(c *Config)
		params       string
		wantRollback int64
		wantSummary  string
	}{
		{
			name:         "one back",
			params:       oneBack,
			wantRollback: 6,
			wantSummary:  "Rolled back application shop/checkout from cccc333 to bbbb222. Argo CD recorded it as deployment 8.",
		},
		{
			name:         "two back",
			change:       allowBack(2),
			params:       twoBack,
			wantRollback: 5,
			wantSummary:  "Rolled back application shop/checkout from cccc333 to aaaa111. Argo CD recorded it as deployment 8.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeArgo(t)
			a := newTestAction(t, api, tt.change)
			snap := snapshotOf(t, a, tt.params)

			res, err := a.Execute(t.Context(), invocationOf(tt.params, &snap))
			require.NoError(t, err)
			require.Equal(t, tt.wantSummary, res.Summary)
			require.Equal(t, []action.Link{{Title: "Application shop/checkout", URL: api.URL() + "/applications/argocd/checkout"}}, res.Links)
			require.Equal(t, []int64{tt.wantRollback}, f.rolledBack())
			require.JSONEq(t, `{"id": `+strconv.FormatInt(tt.wantRollback, 10)+`, "project": "shop", "appNamespace": "argocd", "prune": false}`, string(f.bodies[0]))

			for _, r := range api.Requests() {
				require.Equal(t, "Bearer "+testToken, r.Header.Get("Authorization"))
			}
		})
	}
}

func TestExecuteTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	a := newTestAction(t, api, nil)
	snap := snapshotOf(t, a, oneBack)
	inv := invocationOf(oneBack, &snap)

	first, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	require.Equal(t, first.Summary, second.Summary)
	require.Equal(t, []int64{6}, f.rolledBack(), "the second run asks for nothing")
}

func TestExecuteAfterACrashWhileTheOperationRuns(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	a := newTestAction(t, api, nil)
	snap := snapshotOf(t, a, oneBack)

	f.update(func(f *fakeArgo) { f.operation, f.polls, f.pollsToEnd = revB, 1, 2 })

	res, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Rolled back application shop/checkout")
	require.Empty(t, f.rolledBack(), "the operation of the first attempt is waited for, not asked for again")
}

func TestExecuteConflicts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(f *fakeArgo)
	}{
		{name: "deployed again with a new revision", change: func(f *fakeArgo) { f.deploy(8, revD, "bob") }},
		{name: "deployed again with the same revision", change: func(f *fakeArgo) { f.deploy(8, revC, "bob") }},
		{name: "the history was cut and the newest entry is another", change: func(f *fakeArgo) { f.history = f.history[:2] }},
		{name: "someone else's operation runs", change: func(f *fakeArgo) { f.operation, f.polls, f.pollsToEnd = revA, 1, -1 }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeArgo(t)
			a := newTestAction(t, api, nil)
			snap := snapshotOf(t, a, oneBack)

			f.update(tt.change)

			_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.Empty(t, f.rolledBack())
		})
	}
}

func TestExecuteStopsWhenAutoSyncTurnedOn(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	a := newTestAction(t, api, nil)
	snap := snapshotOf(t, a, oneBack)

	f.update(func(f *fakeArgo) { f.autoSync = true })

	_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "refuses a rollback while auto-sync is on")
	require.Empty(t, f.rolledBack())
}

func TestExecuteRefusesWhatChangedToUnsupported(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(f *fakeArgo)
		want   string
	}{
		{name: "several sources", change: func(f *fakeArgo) { f.sources = 2 }, want: "has several sources"},
		{name: "the source hydrator", change: func(f *fakeArgo) { f.hydrator = true }, want: "uses the source hydrator"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeArgo(t)
			a := newTestAction(t, api, nil)
			snap := snapshotOf(t, a, oneBack)

			f.update(tt.change)

			_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.want)
			require.Empty(t, f.rolledBack())
		})
	}
}

func TestExecuteWhenTheHistoryEntryIsGone(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	a := newTestAction(t, api, nil)
	snap := snapshotOf(t, a, oneBack)

	f.update(func(f *fakeArgo) { f.history = []map[string]any{f.history[0], f.history[2]} })

	_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "history entry 6 of application shop/checkout is gone")
	require.ErrorContains(t, err, "revisionHistoryLimit")
}

func TestExecuteOperationEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		phase         string
		message       string
		wantPermanent bool
		wantErr       string
	}{
		{name: "it succeeds", phase: "Succeeded"},
		{name: "it fails", phase: "Failed", message: "one or more objects failed to apply", wantPermanent: true, wantErr: "ended with phase Failed in Argo CD: one or more objects failed to apply"},
		{name: "it ends with an error", phase: "Error", message: "ComparisonError", wantPermanent: true, wantErr: "ended with phase Error in Argo CD: ComparisonError"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeArgo(t)
			f.update(func(f *fakeArgo) { f.phase, f.message = tt.phase, tt.message })

			a := newTestAction(t, api, nil)
			snap := snapshotOf(t, a, oneBack)

			_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrPermanent)
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, []int64{6}, f.rolledBack())
		})
	}
}

func TestExecuteOperationThatDoesNotEnd(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	f.update(func(f *fakeArgo) { f.pollsToEnd = -1 })

	a := newTestAction(t, api, nil)
	a.waitFor = 200 * time.Millisecond
	snap := snapshotOf(t, a, oneBack)

	_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrPermanent, "a later attempt picks the operation up")
	require.NotErrorIs(t, err, action.ErrConflict)
	require.ErrorContains(t, err, "had not ended in Argo CD after 200ms (phase Running)")
	require.Equal(t, []int64{6}, f.rolledBack())

	// The attempt after it finds the operation and asks for nothing.
	f.update(func(f *fakeArgo) { f.pollsToEnd = 1 })

	res, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Rolled back")
	require.Equal(t, []int64{6}, f.rolledBack())
}

func TestExecuteErrorsOfTheRollbackCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		failure       fakeFailure
		wantPermanent bool
		wantConflict  bool
	}{
		{name: "the token may not sync", failure: fakeFailure{http.StatusForbidden, "permission denied"}, wantPermanent: true},
		{name: "Argo CD refuses with a 400", failure: fakeFailure{http.StatusBadRequest, "rollback cannot be initiated when auto-sync is enabled"}, wantPermanent: true},
		{name: "another operation runs", failure: fakeFailure{http.StatusBadRequest, "another operation is already in progress"}, wantConflict: true},
		{name: "a server error", failure: fakeFailure{http.StatusInternalServerError, "boom"}},
		{name: "a bad gateway", failure: fakeFailure{http.StatusBadGateway, "bad gateway"}},
		{name: "too many requests", failure: fakeFailure{http.StatusTooManyRequests, "slow down"}},
		{name: "a request timeout", failure: fakeFailure{http.StatusRequestTimeout, "timeout"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeArgo(t)
			a := newTestAction(t, api, nil)
			snap := snapshotOf(t, a, oneBack)

			f.update(func(f *fakeArgo) { f.rollbackFailure = &tt.failure })

			_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.Equal(t, tt.wantConflict, errors.Is(err, action.ErrConflict))
			require.ErrorContains(t, err, tt.failure.message)
		})
	}
}

func TestExecuteReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		failure       fakeFailure
		wantPermanent bool
	}{
		{name: "the application is gone", failure: fakeFailure{http.StatusNotFound, "not found"}, wantPermanent: true},
		{name: "a refused token", failure: fakeFailure{http.StatusUnauthorized, "invalid session"}, wantPermanent: true},
		{name: "a server error", failure: fakeFailure{http.StatusServiceUnavailable, "down"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api, f := newFakeArgo(t)
			a := newTestAction(t, api, nil)
			snap := snapshotOf(t, a, oneBack)

			f.update(func(f *fakeArgo) { f.getFailure = &tt.failure })

			_, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
			require.Empty(t, f.rolledBack())
		})
	}
}

func TestExecuteChecksTheLimitsAgain(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	wide := newTestAction(t, api, allowBack(2))
	snap := snapshotOf(t, wide, twoBack)

	narrow := newTestAction(t, api, nil)

	_, err := narrow.Execute(t.Context(), invocationOf(twoBack, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, f.rolledBack())
}

func TestExecuteNeedsItsSnapshot(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	a := newTestAction(t, api, nil)
	snap := snapshotOf(t, a, oneBack)

	other := snap
	other.Target = "argocd:application/shop/cart"

	notARollback := snap
	notARollback.Revision = false

	badVersion := snap
	badVersion.Version = "seven"

	tests := []struct {
		name string
		snap *action.Snapshot
	}{
		{name: "no snapshot"},
		{name: "a snapshot of another application", snap: &other},
		{name: "a snapshot that is not a rollback's", snap: &notARollback},
		{name: "a version that is not an ID and a revision", snap: &badVersion},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := a.Execute(t.Context(), invocationOf(oneBack, tt.snap))
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Empty(t, f.rolledBack())
		})
	}
}

func TestExecuteKnowsTheRollbackWhenTheOlderEntryIsGone(t *testing.T) {
	t.Parallel()

	api, f := newFakeArgo(t)
	a := newTestAction(t, api, nil)
	snap := snapshotOf(t, a, oneBack)

	// The rollback was made, and Argo CD, which keeps two entries here, has
	// dropped the entry it rolled back to.
	f.update(func(f *fakeArgo) {
		f.deploy(8, revB, "sidekick")
		f.history = f.history[2:]
	})

	res, err := a.Execute(t.Context(), invocationOf(oneBack, &snap))
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Rolled back application shop/checkout")
	require.Empty(t, f.rolledBack())
}

func TestOperationPhase(t *testing.T) {
	t.Parallel()

	pending := &argocd.Application{Operation: &argocd.Operation{}}
	require.Equal(t, "Pending", operationPhase(pending))

	running := &argocd.Application{Status: argocd.Status{OperationState: &argocd.OperationState{Phase: "Terminating"}}}
	require.Equal(t, "Terminating", operationPhase(running))

	stale := &argocd.Application{Operation: &argocd.Operation{}, Status: argocd.Status{OperationState: &argocd.OperationState{Phase: "Succeeded"}}}
	require.Equal(t, "Pending", operationPhase(stale))
}

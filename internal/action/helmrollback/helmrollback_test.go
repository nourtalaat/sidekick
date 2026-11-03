package helmrollback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	helmaction "helm.sh/helm/v4/pkg/action"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/kube"
	kubefake "helm.sh/helm/v4/pkg/kube/fake"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"
	"helm.sh/helm/v4/pkg/storage"
	"helm.sh/helm/v4/pkg/storage/driver"

	"github.com/nourtalaat/sidekick/internal/action"
)

const (
	testNamespace = "shop"
	testRelease   = "web"
	paramsOneBack = `{"release":"web","namespace":"shop"}`
)

// firstDeployed is when the fake release was first deployed. Revision n was
// deployed n hours later.
var firstDeployed = time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

// fakeCluster is Helm with its in-memory store and a fake Kubernetes client,
// as Helm's own tests use them.
type fakeCluster struct {
	t    *testing.T
	cfg  *helmaction.Configuration
	kube *kubefake.FailingKubeClient
}

func newFakeCluster(t *testing.T) *fakeCluster {
	t.Helper()

	kc := &kubefake.FailingKubeClient{PrintingKubeClient: kubefake.PrintingKubeClient{Out: io.Discard}}

	cfg := helmaction.NewConfiguration(helmaction.ConfigurationSetLogger(slog.DiscardHandler))
	cfg.Releases = storage.Init(driver.NewMemory())
	cfg.KubeClient = kc
	cfg.SetHookOutputFunc(func(_, _, _ string) io.Writer { return io.Discard })

	return &fakeCluster{t: t, cfg: cfg, kube: kc}
}

// opener opens the fake cluster. Every session shares one store, as the
// calls of a real cluster share the release records.
func (f *fakeCluster) opener() opener {
	return func(_ context.Context, namespace string) (*session, error) {
		if namespace != testNamespace {
			return nil, fmt.Errorf("the fake cluster has no namespace %q", namespace)
		}

		return &session{cfg: f.cfg}, nil
	}
}

// add stores revision version of the fake release with a status. A change
// can set more of the record.
func (f *fakeCluster) add(version int, status common.Status, changes ...func(*releasev1.Release)) {
	f.t.Helper()

	rel := &releasev1.Release{
		Name:        testRelease,
		Namespace:   testNamespace,
		Version:     version,
		ApplyMethod: "csa",
		Manifest:    fmt.Sprintf("kind: ConfigMap\nmetadata:\n  name: web-%d\n", version),
		Info: &releasev1.Info{
			Status:        status,
			FirstDeployed: firstDeployed,
			LastDeployed:  firstDeployed.Add(time.Duration(version) * time.Hour),
			Description:   fmt.Sprintf("Upgrade complete %d", version),
		},
		Chart: &chartv2.Chart{Metadata: &chartv2.Metadata{
			Name: "web", Version: fmt.Sprintf("1.%d.0", version), AppVersion: fmt.Sprintf("2.%d", version),
		}},
	}

	for _, change := range changes {
		change(rel)
	}

	require.NoError(f.t, f.cfg.Releases.Create(rel))
}

// standard stores the history of a plain release: revisions 1 and 2 are
// superseded and revision 3 runs.
func (f *fakeCluster) standard() {
	f.t.Helper()

	f.add(1, common.StatusSuperseded)
	f.add(2, common.StatusSuperseded)
	f.add(3, common.StatusDeployed)
}

// history returns the revisions of the fake release, oldest first.
func (f *fakeCluster) history() []revision {
	f.t.Helper()

	revs, err := (&session{cfg: f.cfg}).history(testRelease)
	require.NoError(f.t, err)

	return revs
}

// newest returns the newest revision of the fake release.
func (f *fakeCluster) newest() revision {
	f.t.Helper()

	revs := f.history()

	return revs[len(revs)-1]
}

// status returns the status of one revision.
func (f *fakeCluster) status(version int) common.Status {
	f.t.Helper()

	for _, r := range f.history() {
		if r.version == version {
			return r.status
		}
	}

	f.t.Fatalf("revision %d is not in the history", version)

	return ""
}

// config returns a config for the fake release, which a change can alter.
func config(change func(*Config)) Config {
	cfg := Config{Limits: Limits{Namespaces: []string{testNamespace}, Releases: []string{testRelease, "api-*"}}}
	if change != nil {
		change(&cfg)
	}

	return cfg
}

// newAction builds the action around the fake cluster.
func newAction(t *testing.T, f *fakeCluster, change func(*Config)) *Rollback {
	t.Helper()

	cfg := config(change)
	require.NoError(t, cfg.Validate())

	return newRollback("helm-web", cfg, f.opener())
}

func paramsBack(back int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"release":"web","namespace":"shop","revisionsBack":%d}`, back))
}

// snapshotOf takes the snapshot of a proposal.
func snapshotOf(t *testing.T, a *Rollback, params json.RawMessage) action.Snapshot {
	t.Helper()

	snap, err := a.Snapshot(t.Context(), params)
	require.NoError(t, err)

	return snap
}

// invocation makes the invocation of an approved proposal.
func invocation(params json.RawMessage, snap *action.Snapshot) action.Invocation {
	return action.Invocation{ProposalID: "proposal-1", Params: params, IdempotencyKey: "key-1", ApprovedBy: "user-1", Snapshot: snap}
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		params  string
		wantErr string
		wantIs  error
	}{
		{name: "the smallest proposal", params: paramsOneBack},
		{name: "one back written", params: `{"release":"web","namespace":"shop","revisionsBack":1}`},
		{name: "a release that a pattern allows", params: `{"release":"api-orders","namespace":"shop"}`},
		{
			name: "two back with the limit at two", params: `{"release":"web","namespace":"shop","revisionsBack":2}`,
			change: func(c *Config) { c.MaxRevisionsBack = new(2) },
		},
		{
			name: "two back with the default limit", params: `{"release":"web","namespace":"shop","revisionsBack":2}`,
			wantErr: "2 revisions back is more than maxRevisionsBack allows (1)", wantIs: action.ErrLimit,
		},
		{
			name: "three back with the limit at two", params: `{"release":"web","namespace":"shop","revisionsBack":3}`,
			change:  func(c *Config) { c.MaxRevisionsBack = new(2) },
			wantErr: "3 revisions back is more than maxRevisionsBack allows (2)", wantIs: action.ErrLimit,
		},
		{
			name: "zero back", params: `{"release":"web","namespace":"shop","revisionsBack":0}`,
			wantErr: "0 revisions back", wantIs: action.ErrParams,
		},
		{
			name: "a negative number back", params: `{"release":"web","namespace":"shop","revisionsBack":-1}`,
			wantErr: "-1 revisions back", wantIs: action.ErrParams,
		},
		{name: "another namespace", params: `{"release":"web","namespace":"kube-system"}`, wantErr: `namespace "kube-system" is not one of limits.namespaces`, wantIs: action.ErrLimit},
		{name: "another release", params: `{"release":"db","namespace":"shop"}`, wantErr: `release "db" is not allowed by limits.releases`, wantIs: action.ErrLimit},
		{name: "a release that is not a Helm name", params: `{"release":"Web_1","namespace":"shop"}`, change: func(c *Config) { c.Limits.Releases = []string{"*"} }, wantErr: `release "Web_1" is not a valid Helm release name`, wantIs: action.ErrParams},
		{name: "no release", params: `{"namespace":"shop"}`, wantErr: "is not a valid Helm release name", wantIs: action.ErrParams},
		{name: "force is not a parameter", params: `{"release":"web","namespace":"shop","force":true}`, wantErr: "unknown field", wantIs: action.ErrParams},
		{name: "parameters that are not JSON", params: `[]`, wantErr: "decode the parameters", wantIs: action.ErrParams},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			a := newAction(t, newFakeCluster(t), tt.change)

			err := a.Validate(t.Context(), json.RawMessage(tt.params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, tt.wantIs)
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	a := newAction(t, newFakeCluster(t), func(c *Config) { c.MaxRevisionsBack = new(3) })
	spec := a.Spec()

	require.Equal(t, "helm-web", spec.Name)
	require.Contains(t, spec.Description, "Allowed namespaces: shop.")
	require.Contains(t, spec.Description, "Allowed releases: web, api-*")
	require.Contains(t, spec.Description, "At most 3 revisions back.")

	var schema struct {
		Required   []string `json:"required"`
		Properties map[string]struct {
			Maximum int `json:"maximum"`
		} `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(spec.ParamsSchema, &schema))
	require.Equal(t, []string{"release", "namespace"}, schema.Required)
	require.Equal(t, 3, schema.Properties["revisionsBack"].Maximum)
}

func TestApprovalOnly(t *testing.T) {
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

			a := newAction(t, newFakeCluster(t), tt.change)

			require.Equal(t, tt.want, action.ReadWriteBlocked(a), "the built action reports it")
		})
	}
}

func TestSnapshotOneBack(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	snap, err := newAction(t, f, nil).Snapshot(t.Context(), json.RawMessage(paramsOneBack))
	require.NoError(t, err)
	require.True(t, snap.Revision)
	require.Equal(t, "helm:release/shop/web", snap.Target)
	require.Equal(t, "3", snap.Version, "the version is the revision that runs")

	revisions, err := snap.Revisions()
	require.NoError(t, err)

	running := firstDeployed.Add(3 * time.Hour)
	goesBack := firstDeployed.Add(2 * time.Hour)

	require.Equal(t, action.Revision{
		Kind: "Helm release revision", ID: "3", Version: "web-1.3.0 (app 2.3)", DeployedAt: &running,
		Notes: "Helm status: deployed. Helm does not record who deployed it. Description: Upgrade complete 3",
	}, revisions.Running)
	require.Equal(t, action.Revision{
		Kind: "Helm release revision", ID: "2", Version: "web-1.2.0 (app 2.2)", DeployedAt: &goesBack,
		Notes: "Helm status: superseded. Helm does not record who deployed it. Description: Upgrade complete 2",
	}, revisions.GoesBackTo)
	require.Empty(t, revisions.Running.DeployedBy, "Helm does not record who deployed")
}

func TestSnapshotChanges(t *testing.T) {
	t.Parallel()

	twoBack := func(c *Config) { c.MaxRevisionsBack = new(2) }

	tests := []struct {
		name        string
		history     func(f *fakeCluster)
		change      func(c *Config)
		params      json.RawMessage
		wantRunning string
		wantTarget  string
		wantErr     string
	}{
		{name: "two back", history: (*fakeCluster).standard, change: twoBack, params: paramsBack(2), wantRunning: "3", wantTarget: "1"},
		{
			name: "two back with a limit of one", history: (*fakeCluster).standard, params: paramsBack(2),
			wantErr: "2 revisions back is more than maxRevisionsBack allows (1)",
		},
		{
			name: "failed revisions are skipped", change: twoBack, params: paramsBack(2), wantRunning: "5", wantTarget: "1",
			history: func(f *fakeCluster) {
				f.add(1, common.StatusSuperseded)
				f.add(2, common.StatusFailed)
				f.add(3, common.StatusSuperseded)
				f.add(4, common.StatusFailed)
				f.add(5, common.StatusDeployed)
			},
		},
		{
			name: "a failed upgrade runs, and the deployed revision before it is one back", params: paramsBack(1), wantRunning: "3", wantTarget: "2",
			history: func(f *fakeCluster) {
				f.add(1, common.StatusSuperseded)
				f.add(2, common.StatusDeployed)
				f.add(3, common.StatusFailed)
			},
		},
		{
			name: "a release with a pending upgrade", params: paramsBack(1),
			history: func(f *fakeCluster) {
				f.add(1, common.StatusSuperseded)
				f.add(2, common.StatusPendingUpgrade)
			},
			wantErr: `release "web" has revision 2 in status pending-upgrade`,
		},
		{
			name: "a release that a crash left in pending-rollback", params: paramsBack(1),
			history: func(f *fakeCluster) {
				f.add(1, common.StatusSuperseded)
				f.add(2, common.StatusDeployed)
				f.add(3, common.StatusPendingRollback)
			},
			wantErr: `release "web" has revision 3 in status pending-rollback`,
		},
		{
			name: "an uninstalled release", params: paramsBack(1),
			history: func(f *fakeCluster) {
				f.add(1, common.StatusSuperseded)
				f.add(2, common.StatusUninstalled)
			},
			wantErr: "has status uninstalled",
		},
		{
			name: "a release with one revision", params: paramsBack(1),
			history: func(f *fakeCluster) { f.add(1, common.StatusDeployed) },
			wantErr: "has no earlier revision with status superseded or deployed",
		},
		{name: "a release that is not there", params: paramsBack(1), history: func(*fakeCluster) {}, wantErr: `release "web" was not found in namespace "shop"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeCluster(t)
			tt.history(f)

			snap, err := newAction(t, f, tt.change).Snapshot(t.Context(), tt.params)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)

			revisions, err := snap.Revisions()
			require.NoError(t, err)
			require.Equal(t, tt.wantRunning, revisions.Running.ID)
			require.Equal(t, tt.wantTarget, revisions.GoesBackTo.ID)
		})
	}
}

func TestSnapshotChecksTheLimits(t *testing.T) {
	t.Parallel()

	a := newAction(t, newFakeCluster(t), nil)

	_, err := a.Snapshot(t.Context(), json.RawMessage(`{"release":"db","namespace":"shop"}`))
	require.ErrorIs(t, err, action.ErrLimit)
}

func TestSnapshotWritesNothing(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	_ = snapshotOf(t, newAction(t, f, nil), json.RawMessage(paramsOneBack))
	require.Len(t, f.history(), 3)
	require.Empty(t, f.kube.RecordedWaitStrategies)
}

func TestExecute(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		change       func(c *Config)
		wantStrategy kube.WaitStrategy
	}{
		{name: "with the wait on", wantStrategy: kube.StatusWatcherStrategy},
		{name: "with the wait off", change: func(c *Config) { c.Wait = new(false) }, wantStrategy: kube.HookOnlyStrategy},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeCluster(t)
			f.standard()

			a := newAction(t, f, tt.change)
			snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))

			res, err := a.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), &snap))
			require.NoError(t, err)
			require.Equal(t, `Rolled back Helm release "web" in namespace "shop" from revision 3 to revision 2 (Helm made revision 4).`, res.Summary)

			revs := f.history()
			require.Len(t, revs, 4, "Helm made one new revision")
			require.Equal(t, common.StatusSuperseded, revs[2].status)
			require.Equal(t, common.StatusDeployed, revs[3].status)
			require.Equal(t, 2, revs[3].rollbackRevision)
			require.Equal(t, "Rollback to 2 by Sidekick (proposal proposal-1)", revs[3].description)
			require.Equal(t, "web", revs[3].chartName)
			require.Equal(t, "1.2.0", revs[3].chartVersion, "the new revision has the chart of the target")

			require.Equal(t, []kube.WaitStrategy{tt.wantStrategy}, f.kube.RecordedWaitStrategies)
		})
	}
}

func TestExecuteTwoBack(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, func(c *Config) { c.MaxRevisionsBack = new(2) })
	snap := snapshotOf(t, a, paramsBack(2))

	_, err := a.Execute(t.Context(), invocation(paramsBack(2), &snap))
	require.NoError(t, err)
	require.Equal(t, 1, f.newest().rollbackRevision)
	require.Equal(t, "1.1.0", f.newest().chartVersion)
}

func TestExecuteTwiceChangesOnce(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))
	inv := invocation(json.RawMessage(paramsOneBack), &snap)

	first, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Len(t, f.history(), 4, "the retry made no second revision")
}

func TestExecuteAfterACrashThatLeftTheRollbackInPlace(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))

	// Helm made the rollback, and the worker died before it stored the result.
	require.NoError(t, f.cfg.Releases.Update(mustRelease(t, f, 3, common.StatusSuperseded)))
	f.add(4, common.StatusDeployed, func(r *releasev1.Release) { r.Info.RollbackRevision = 2 })

	res, err := a.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), &snap))
	require.NoError(t, err)
	require.Contains(t, res.Summary, "(Helm made revision 4)")
	require.Len(t, f.history(), 4)
	require.Empty(t, f.kube.RecordedWaitStrategies, "nothing ran")
}

// mustRelease returns the stored record of a revision of the fake release,
// with a new status.
func mustRelease(t *testing.T, f *fakeCluster, version int, status common.Status) *releasev1.Release {
	t.Helper()

	got, err := f.cfg.Releases.Get(testRelease, version)
	require.NoError(t, err)

	rel, ok := got.(*releasev1.Release)
	require.True(t, ok)

	rel.Info.Status = status

	return rel
}

func TestExecuteFailsWithAConflictAfterAnotherDeploy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		after func(f *fakeCluster)
	}{
		{name: "an upgrade", after: func(f *fakeCluster) {
			require.NoError(t, f.cfg.Releases.Update(mustRelease(t, f, 3, common.StatusSuperseded)))
			f.add(4, common.StatusDeployed)
		}},
		{name: "an upgrade that failed", after: func(f *fakeCluster) { f.add(4, common.StatusFailed) }},
		{name: "a rollback to another revision", after: func(f *fakeCluster) {
			require.NoError(t, f.cfg.Releases.Update(mustRelease(t, f, 3, common.StatusSuperseded)))
			f.add(4, common.StatusDeployed, func(r *releasev1.Release) { r.Info.RollbackRevision = 1 })
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeCluster(t)
			f.standard()

			a := newAction(t, f, nil)
			snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))

			tt.after(f)

			_, err := a.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), &snap))
			require.ErrorIs(t, err, action.ErrConflict)
			require.ErrorContains(t, err, `release "web" changed since the proposal`)
			require.Len(t, f.history(), 4, "nothing was rolled back")
			require.Empty(t, f.kube.RecordedWaitStrategies)
		})
	}
}

func TestExecuteFailsWhenTheTargetIsGone(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))

	require.NoError(t, f.cfg.Releases.Update(mustRelease(t, f, 2, common.StatusFailed)))

	_, err := a.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), &snap))
	require.ErrorIs(t, err, action.ErrConflict)
	require.ErrorContains(t, err, "revision 2 of release \"web\" is not in its history any more")
}

func TestExecuteWhenTheWaitFails(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()
	f.kube.WaitError = errors.New("timed out waiting for the condition")

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))
	inv := invocation(json.RawMessage(paramsOneBack), &snap)

	_, err := a.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "made revision 4, which ended with status failed")
	require.ErrorContains(t, err, "timed out waiting for the condition")
	require.Equal(t, common.StatusFailed, f.newest().status)
	require.Equal(t, common.StatusDeployed, f.status(3), "Helm leaves the old revision deployed when the wait fails")

	f.kube.WaitError = nil

	_, err = a.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrPermanent, "a retry does not make more revisions")
	require.ErrorContains(t, err, "an earlier try rolled back release")
	require.Len(t, f.history(), 4)
}

func TestExecuteWhenApplyingFails(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()
	f.kube.UpdateError = errors.New("the apply was refused")

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))

	_, err := a.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), &snap))
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "the apply was refused")
	require.Equal(t, common.StatusFailed, f.newest().status)
	require.Equal(t, common.StatusSuperseded, f.status(3))
}

func TestExecuteWhenAHookFailsLeavesAPendingRollback(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.add(1, common.StatusSuperseded)
	f.add(2, common.StatusSuperseded, func(r *releasev1.Release) {
		r.Hooks = []*releasev1.Hook{{
			Name: "migrate", Kind: "Job", Path: "templates/hook.yaml", Manifest: "kind: Job\nmetadata:\n  name: migrate\n",
			Events: []releasev1.HookEvent{releasev1.HookPreRollback},
		}}
	})
	f.add(3, common.StatusDeployed)
	f.kube.WatchUntilReadyError = errors.New("the hook job failed")

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))
	inv := invocation(json.RawMessage(paramsOneBack), &snap)

	_, err := a.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "Secret sh.helm.release.v1.web.v4")
	require.Equal(t, common.StatusPendingRollback, f.newest().status)

	_, err = a.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrPermanent, "an interrupted rollback is never repeated")
	require.Len(t, f.history(), 4)
}

func TestExecuteWhenTheClusterCannotBeReachedMayBeTriedAgain(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))

	f.kube.ConnectionError = errors.New("connection refused")

	_, err := a.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), &snap))
	require.ErrorContains(t, err, "will be tried again")
	require.NotErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrConflict)

	f.kube.ConnectionError = nil

	require.Len(t, f.history(), 3, "nothing was written")
}

func TestExecuteChecksTheLimitsAgain(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	snap := snapshotOf(t, newAction(t, f, nil), json.RawMessage(paramsOneBack))
	narrower := newAction(t, f, func(c *Config) { c.Limits.Releases = []string{"api-*"} })

	_, err := narrower.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.Len(t, f.history(), 3)
}

func TestExecuteChecksTheSnapshot(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	good := snapshotOf(t, a, json.RawMessage(paramsOneBack))

	otherRelease := good
	otherRelease.Target = "helm:release/shop/api"

	wrongVersion := good
	wrongVersion.Version = "2"

	notARollback := action.Snapshot{Target: good.Target, Before: good.Before, After: good.After, Version: good.Version}

	goesForward, err := action.RevisionSnapshot(good.Target, "2", mustRevision(t, 2), mustRevision(t, 3))
	require.NoError(t, err)

	tests := []struct {
		name string
		snap *action.Snapshot
	}{
		{name: "no snapshot"},
		{name: "a snapshot of another release", snap: &otherRelease},
		{name: "a version that is not the running revision", snap: &wrongVersion},
		{name: "a snapshot that is not a rollback", snap: &notARollback},
		{name: "a snapshot that goes forward", snap: &goesForward},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := a.Execute(t.Context(), invocation(json.RawMessage(paramsOneBack), tt.snap))
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Len(t, f.history(), 3)
		})
	}
}

// mustRevision makes the document of a revision number.
func mustRevision(t *testing.T, version int) action.Revision {
	t.Helper()

	return revision{version: version, status: common.StatusDeployed}.document()
}

func TestUndo(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))
	inv := invocation(json.RawMessage(paramsOneBack), &snap)

	_, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	undoInv := inv
	undoInv.ProposalID, undoInv.UndoOf = "proposal-2", "proposal-1"

	res, err := a.Undo(t.Context(), undoInv, snap, false)
	require.NoError(t, err)
	require.Equal(t, `Rolled back Helm release "web" in namespace "shop" to the state of revision 3 (Helm made revision 5).`, res.Summary)

	revs := f.history()
	require.Len(t, revs, 5, "Helm made a new revision again")
	require.Equal(t, common.StatusDeployed, revs[4].status)
	require.Equal(t, 3, revs[4].rollbackRevision)
	require.Equal(t, "1.3.0", revs[4].chartVersion, "the chart of the revision that ran before")
	require.Equal(t, "Rollback to 3 by Sidekick (undo of proposal proposal-1)", revs[4].description)

	strategies := f.kube.RecordedWaitStrategies
	require.Equal(t, kube.HookOnlyStrategy, strategies[len(strategies)-1], "an undo does not wait for the objects")

	again, err := a.Undo(t.Context(), undoInv, snap, false)
	require.NoError(t, err, "an undo can be repeated")
	require.Equal(t, res, again)
	require.Len(t, f.history(), 5)
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		after func(f *fakeCluster)
		live  string
	}{
		{name: "an upgrade after the rollback", live: "5", after: func(f *fakeCluster) {
			require.NoError(t, f.cfg.Releases.Update(mustRelease(t, f, 4, common.StatusSuperseded)))
			f.add(5, common.StatusDeployed)
		}},
		{name: "an upgrade after the rollback that failed", live: "5", after: func(f *fakeCluster) { f.add(5, common.StatusFailed) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			f := newFakeCluster(t)
			f.standard()

			a := newAction(t, f, nil)
			snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))
			inv := invocation(json.RawMessage(paramsOneBack), &snap)

			_, err := a.Execute(t.Context(), inv)
			require.NoError(t, err)

			tt.after(f)

			_, err = a.Undo(t.Context(), inv, snap, false)
			require.ErrorIs(t, err, action.ErrConflict)

			conflict, ok := errors.AsType[*action.ConflictError](err)
			require.True(t, ok, "the conflict carries the live revision")

			var live action.Revision
			require.NoError(t, json.Unmarshal(conflict.Live, &live))
			require.Equal(t, tt.live, live.ID)
			require.Equal(t, "Helm release revision", live.Kind)
			require.Len(t, f.history(), 5, "nothing was restored")

			res, err := a.Undo(t.Context(), inv, snap, true)
			require.NoError(t, err, "force restores it anyway")
			require.Contains(t, res.Summary, "(Helm made revision 6)")
			require.Equal(t, 3, f.newest().rollbackRevision)
			require.Len(t, f.history(), 6)
		})
	}
}

func TestUndoFailsWhenTheRevisionToRestoreIsGone(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))
	inv := invocation(json.RawMessage(paramsOneBack), &snap)

	_, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	require.NoError(t, f.cfg.Releases.Update(mustRelease(t, f, 3, common.StatusFailed)))

	_, err = a.Undo(t.Context(), inv, snap, true)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, `revision 3 of release "web" is not in its history any more`)
}

func TestUndoRefusesAReleaseWithAPendingOperation(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)
	snap := snapshotOf(t, a, json.RawMessage(paramsOneBack))
	inv := invocation(json.RawMessage(paramsOneBack), &snap)

	_, err := a.Execute(t.Context(), inv)
	require.NoError(t, err)

	f.add(5, common.StatusPendingUpgrade)

	_, err = a.Undo(t.Context(), inv, snap, true)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "revision 5 in status pending-upgrade")
}

func TestUndoChecksTheSnapshot(t *testing.T) {
	t.Parallel()

	f := newFakeCluster(t)
	f.standard()

	a := newAction(t, f, nil)

	_, err := a.Undo(t.Context(), invocation(json.RawMessage(paramsOneBack), nil), action.Snapshot{}, false)
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantIs    error
		wantNotIs error
	}{
		{name: "the next revision was written first", err: fmt.Errorf("create: %w", driver.ErrReleaseExists), wantIs: action.ErrConflict},
		{name: "an error that may pass", err: errors.New("connection reset"), wantNotIs: action.ErrPermanent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classify(tt.err)
			require.ErrorIs(t, got, tt.err)

			if tt.wantIs != nil {
				require.ErrorIs(t, got, tt.wantIs)
			}

			if tt.wantNotIs != nil {
				require.NotErrorIs(t, got, tt.wantNotIs)
				require.ErrorContains(t, got, "will be tried again")
			}
		})
	}
}

func TestMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern, release string
		want             bool
	}{
		{pattern: "web", release: "web", want: true},
		{pattern: "web", release: "web-2"},
		{pattern: "web-*", release: "web-2", want: true},
		{pattern: "web-*", release: "web"},
		{pattern: "*", release: "anything.at-all", want: true},
		{pattern: "a*c", release: "abbbc", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.release, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, matches(tt.pattern, tt.release))
		})
	}
}

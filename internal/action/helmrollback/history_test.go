package helmrollback

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	chartv2 "helm.sh/helm/v4/pkg/chart/v2"
	"helm.sh/helm/v4/pkg/release"
	"helm.sh/helm/v4/pkg/release/common"
	releasev1 "helm.sh/helm/v4/pkg/release/v1"

	"github.com/nourtalaat/sidekick/internal/action"
)

// rev makes a revision with a status, for a test of the history.
func rev(version int, status common.Status) revision {
	return revision{version: version, status: status}
}

func TestChoose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		revs        []revision
		back        int
		wantRunning int
		wantTarget  int
		wantErr     string
	}{
		{
			name: "one back from a plain history",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusSuperseded), rev(3, common.StatusDeployed)},
			back: 1, wantRunning: 3, wantTarget: 2,
		},
		{
			name: "two back",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusSuperseded), rev(3, common.StatusDeployed)},
			back: 2, wantRunning: 3, wantTarget: 1,
		},
		{
			name: "failed revisions before the running one are skipped",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusFailed), rev(3, common.StatusFailed), rev(4, common.StatusDeployed)},
			back: 1, wantRunning: 4, wantTarget: 1,
		},
		{
			name: "pending revisions before the running one are skipped",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusPendingUpgrade), rev(3, common.StatusSuperseded), rev(4, common.StatusDeployed)},
			back: 2, wantRunning: 4, wantTarget: 1,
		},
		{
			name: "a failed upgrade leaves the earlier revision deployed, and the failed one runs",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusDeployed), rev(3, common.StatusFailed)},
			back: 1, wantRunning: 3, wantTarget: 2,
		},
		{
			name: "a failed upgrade, two back",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusDeployed), rev(3, common.StatusFailed)},
			back: 2, wantRunning: 3, wantTarget: 1,
		},
		{
			name: "history that starts after revision 1",
			revs: []revision{rev(7, common.StatusSuperseded), rev(8, common.StatusDeployed)},
			back: 1, wantRunning: 8, wantTarget: 7,
		},
		{name: "no history", back: 1, wantErr: `release "web" was not found in namespace "shop"`},
		{
			name: "a pending upgrade",
			revs: []revision{rev(1, common.StatusDeployed), rev(2, common.StatusPendingUpgrade)},
			back: 1, wantErr: `release "web" has revision 2 in status pending-upgrade: another Helm operation is running on it or was interrupted, so Sidekick will not roll it back; a person has to look at the release first`,
		},
		{
			name: "a pending rollback",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusDeployed), rev(3, common.StatusPendingRollback)},
			back: 1, wantErr: "revision 3 in status pending-rollback",
		},
		{
			name: "a pending install",
			revs: []revision{rev(1, common.StatusPendingInstall)},
			back: 1, wantErr: "revision 1 in status pending-install",
		},
		{
			name: "an uninstalled release",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusUninstalled)},
			back: 1, wantErr: `the newest revision 2 of release "web" has status uninstalled; Sidekick rolls back only a release whose newest revision is deployed or failed`,
		},
		{
			name: "an unknown status",
			revs: []revision{rev(1, common.StatusUnknown)},
			back: 1, wantErr: "has status unknown",
		},
		{
			name: "a release with one revision",
			revs: []revision{rev(1, common.StatusDeployed)},
			back: 1, wantErr: `release "web" is at revision 1 and has no earlier revision with status superseded or deployed to roll back to`,
		},
		{
			name: "only failed revisions before the running one",
			revs: []revision{rev(1, common.StatusFailed), rev(2, common.StatusDeployed)},
			back: 1, wantErr: "has no earlier revision with status superseded or deployed",
		},
		{
			name: "more back than there are revisions",
			revs: []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusFailed), rev(3, common.StatusDeployed)},
			back: 2, wantErr: `release "web" is at revision 3; only 1 earlier revision(s) can be rolled back to (status superseded or deployed): revision 1 (1 back), so it cannot go back 2`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			running, target, err := choose("web", "shop", tt.revs, tt.back)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantRunning, running.version)
			require.Equal(t, tt.wantTarget, target.version)
		})
	}
}

func TestChooseListsAtMostTenRevisions(t *testing.T) {
	t.Parallel()

	revs := []revision{}
	for v := 1; v <= 14; v++ {
		revs = append(revs, rev(v, common.StatusSuperseded))
	}

	revs = append(revs, rev(15, common.StatusDeployed))

	_, _, err := choose("web", "shop", revs, 20)
	require.ErrorContains(t, err, "only 14 earlier revision(s)")
	require.ErrorContains(t, err, "revision 14 (1 back)")
	require.ErrorContains(t, err, "revision 5 (10 back)")
	require.NotContains(t, err.Error(), "revision 4 (11 back)")
}

func TestCheckExecute(t *testing.T) {
	t.Parallel()

	rollback := func(version, to int, status common.Status) revision {
		r := rev(version, status)
		r.rollbackRevision = to
		r.description = "Rollback to 2"

		return r
	}

	base := []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusSuperseded), rev(3, common.StatusDeployed)}

	tests := []struct {
		name     string
		revs     []revision
		wantDone bool
		wantErr  string
		wantIs   error
	}{
		{name: "the release is as it was proposed", revs: base},
		{name: "the running revision failed", revs: []revision{rev(2, common.StatusSuperseded), rev(3, common.StatusFailed)}},
		{name: "an earlier try made the rollback", revs: append(append([]revision{}, base...), rollback(4, 2, common.StatusDeployed)), wantDone: true},
		{
			name:    "an earlier try failed",
			revs:    append(append([]revision{}, base...), rollback(4, 2, common.StatusFailed)),
			wantErr: `an earlier try rolled back release "web" to revision 2 as revision 4 and it ended as failed: Rollback to 2`, wantIs: action.ErrPermanent,
		},
		{
			name:    "an interrupted rollback",
			revs:    append(append([]revision{}, base...), rollback(4, 2, common.StatusPendingRollback)),
			wantErr: "Secret sh.helm.release.v1.web.v4", wantIs: action.ErrPermanent,
		},
		{
			name:    "another operation is running",
			revs:    append(append([]revision{}, base...), rev(4, common.StatusPendingUpgrade)),
			wantErr: "revision 4 in status pending-upgrade", wantIs: action.ErrPermanent,
		},
		{
			name:    "someone upgraded",
			revs:    append(append([]revision{}, base...), rev(4, common.StatusDeployed)),
			wantErr: `release "web" changed since the proposal: its newest revision is 4 (status deployed) and the proposal was made at revision 3`, wantIs: action.ErrConflict,
		},
		{
			name:    "someone upgraded and it failed",
			revs:    append(append([]revision{}, base...), rev(4, common.StatusFailed)),
			wantErr: "its newest revision is 4 (status failed)", wantIs: action.ErrConflict,
		},
		{
			name:    "someone rolled back to another revision",
			revs:    append(append([]revision{}, base...), rollback(4, 1, common.StatusDeployed)),
			wantErr: "its newest revision is 4", wantIs: action.ErrConflict,
		},
		{
			name:    "the target is not in the history any more",
			revs:    []revision{rev(3, common.StatusDeployed)},
			wantErr: "revision 2 of release \"web\" is not in its history any more", wantIs: action.ErrConflict,
		},
		{
			name:    "the target failed",
			revs:    []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusFailed), rev(3, common.StatusDeployed)},
			wantErr: "is not in its history any more, or is not superseded or deployed", wantIs: action.ErrConflict,
		},
		{name: "the release is gone", wantErr: `release "web" is not there any more`, wantIs: action.ErrConflict},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			done, err := checkExecute("web", storeSecret, tt.revs, 3, 2)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, tt.wantIs)
				require.False(t, done)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantDone, done)
		})
	}
}

func TestCheckExecuteNamesTheConfigMap(t *testing.T) {
	t.Parallel()

	revs := []revision{rev(3, common.StatusDeployed), rev(4, common.StatusPendingRollback)}

	_, err := checkExecute("web", storeKind(driverConfigMap), revs, 3, 2)
	require.ErrorContains(t, err, "ConfigMap sh.helm.release.v1.web.v4")
}

func TestCheckUndo(t *testing.T) {
	t.Parallel()

	rollback := func(version, to int, status common.Status) revision {
		r := rev(version, status)
		r.rollbackRevision = to

		return r
	}

	// The rollback went from revision 3 to revision 2 and made revision 4.
	history := []revision{rev(1, common.StatusSuperseded), rev(2, common.StatusSuperseded), rev(3, common.StatusSuperseded), rollback(4, 2, common.StatusDeployed)}

	tests := []struct {
		name      string
		revs      []revision
		want      undoState
		wantLive  int
		wantErr   string
		wantIs    error
		wantNoErr bool
	}{
		{name: "the release is as the rollback left it", revs: history, want: undoGo, wantLive: 4, wantNoErr: true},
		{
			name: "the release was restored already", want: undoDone, wantLive: 5, wantNoErr: true,
			revs: append(append([]revision{}, history[:3]...), rollback(4, 2, common.StatusSuperseded), rollback(5, 3, common.StatusDeployed)),
		},
		{
			name: "someone upgraded after the rollback", want: undoConflict, wantLive: 5, wantNoErr: true,
			revs: append(append([]revision{}, history...), rev(5, common.StatusDeployed)),
		},
		{
			name: "someone rolled back to another revision", want: undoConflict, wantLive: 5, wantNoErr: true,
			revs: append(append([]revision{}, history...), rollback(5, 1, common.StatusDeployed)),
		},
		{
			name: "an upgrade after the rollback failed", want: undoConflict, wantLive: 5, wantNoErr: true,
			revs: append(append([]revision{}, history...), rev(5, common.StatusFailed)),
		},
		{
			name: "another operation is running", wantErr: "revision 5 in status pending-upgrade", wantIs: action.ErrPermanent,
			revs: append(append([]revision{}, history...), rev(5, common.StatusPendingUpgrade)),
		},
		{
			name: "the revision to restore is gone", wantErr: "revision 3 of release \"web\" is not in its history any more", wantIs: action.ErrPermanent,
			revs: []revision{rev(2, common.StatusSuperseded), rollback(4, 2, common.StatusDeployed)},
		},
		{name: "the release is gone", wantErr: `release "web" is not there any more`, wantIs: action.ErrPermanent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			state, live, err := checkUndo("web", storeSecret, tt.revs, 3, 2)
			if !tt.wantNoErr {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, tt.wantIs)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, state)
			require.Equal(t, tt.wantLive, live.version)
		})
	}
}

func TestReadHistory(t *testing.T) {
	t.Parallel()

	newRelease := func(version int, status common.Status) *releasev1.Release {
		return &releasev1.Release{Name: "web", Version: version, Info: &releasev1.Info{Status: status}}
	}

	t.Run("sorts by revision and reads the facts", func(t *testing.T) {
		t.Parallel()

		deployed := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)
		third := newRelease(3, common.StatusDeployed)
		third.Info.LastDeployed = deployed
		third.Info.RollbackRevision = 1
		third.Info.Description = "Rollback to 1"
		third.Chart = &chartv2.Chart{Metadata: &chartv2.Metadata{Name: "web", Version: "1.4.7", AppVersion: "2.9.1"}}

		revs, err := readHistory([]release.Releaser{third, *newRelease(1, common.StatusSuperseded), newRelease(2, common.StatusFailed)})
		require.NoError(t, err)
		require.Len(t, revs, 3)
		require.Equal(t, []int{1, 2, 3}, []int{revs[0].version, revs[1].version, revs[2].version})
		require.Equal(t, revision{
			version: 3, status: common.StatusDeployed, rollbackRevision: 1, deployedAt: deployed, description: "Rollback to 1",
			chartName: "web", chartVersion: "1.4.7", appVersion: "2.9.1",
		}, revs[2])
	})

	t.Run("a record with no info and no chart", func(t *testing.T) {
		t.Parallel()

		revs, err := readHistory([]release.Releaser{&releasev1.Release{Version: 5}})
		require.NoError(t, err)
		require.Equal(t, revision{version: 5, status: common.StatusUnknown}, revs[0])
	})

	t.Run("a record that is not a release", func(t *testing.T) {
		t.Parallel()

		_, err := readHistory([]release.Releaser{"nope"})
		require.ErrorContains(t, err, "not a release")
	})

	t.Run("an empty record", func(t *testing.T) {
		t.Parallel()

		var empty *releasev1.Release

		_, err := readHistory([]release.Releaser{empty})
		require.ErrorContains(t, err, "is empty")
	})
}

func TestRevisionDocument(t *testing.T) {
	t.Parallel()

	deployed := time.Date(2026, 11, 3, 11, 30, 0, 0, time.FixedZone("EET", 2*60*60))

	tests := []struct {
		name string
		rev  revision
		want action.Revision
	}{
		{
			name: "a revision with everything Helm keeps",
			rev: revision{
				version: 7, status: common.StatusSuperseded, deployedAt: deployed, description: "Upgrade complete",
				chartName: "checkout", chartVersion: "1.4.7", appVersion: "2.9.1",
			},
			want: action.Revision{
				Kind: "Helm release revision", ID: "7", Version: "checkout-1.4.7 (app 2.9.1)",
				DeployedAt: new(deployed.UTC()),
				Notes:      "Helm status: superseded. Helm does not record who deployed it. Description: Upgrade complete",
			},
		},
		{
			name: "a chart with no app version",
			rev:  revision{version: 2, status: common.StatusDeployed, chartName: "checkout", chartVersion: "1.0.0"},
			want: action.Revision{Kind: "Helm release revision", ID: "2", Version: "checkout-1.0.0", Notes: "Helm status: deployed. Helm does not record who deployed it."},
		},
		{
			name: "a record with no chart and no time",
			rev:  revision{version: 3, status: common.StatusUnknown},
			want: action.Revision{Kind: "Helm release revision", ID: "3", Notes: "Helm status: unknown. Helm does not record who deployed it."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.rev.document())
		})
	}
}

func TestRevisionDocumentCutsALongDescription(t *testing.T) {
	t.Parallel()

	r := revision{version: 1, status: common.StatusFailed, description: strings.Repeat("é", maxDescriptionRunes+5)}

	require.Equal(t, "Helm status: failed. Helm does not record who deployed it. Description: "+strings.Repeat("é", maxDescriptionRunes)+"...", r.document().Notes)

	r.description = strings.Repeat("é", maxDescriptionRunes)
	require.NotContains(t, r.document().Notes, "...")
}

func TestRevisionSnapshotAcceptsTheDocuments(t *testing.T) {
	t.Parallel()

	snap, err := action.RevisionSnapshot("helm:release/shop/web", "3", rev(3, common.StatusDeployed).document(), rev(2, common.StatusSuperseded).document())
	require.NoError(t, err)

	revisions, err := snap.Revisions()
	require.NoError(t, err)
	require.Equal(t, "3", revisions.Running.ID)
	require.Equal(t, "2", revisions.GoesBackTo.ID)
}

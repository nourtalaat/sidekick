package argocdrollback

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/argocd"
)

// The revisions of the tests: full commit hashes, and a tag.
const (
	revA = "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"
	revB = "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"
	revC = "cccc3333cccc3333cccc3333cccc3333cccc3333"
	revD = "dddd4444dddd4444dddd4444dddd4444dddd4444"
)

// entry makes a history entry.
func entry(id int64, revision string) argocd.HistoryEntry {
	return argocd.HistoryEntry{ID: id, Revision: revision, InitiatedBy: argocd.Initiator{Username: "ana"}}
}

func appWith(entries ...argocd.HistoryEntry) *argocd.Application {
	return &argocd.Application{Status: argocd.Status{History: entries}}
}

func TestPlan(t *testing.T) {
	t.Parallel()

	ref := argocd.AppRef{Project: "shop", Name: "checkout"}

	tests := []struct {
		name        string
		history     []argocd.HistoryEntry
		back        int
		wantRunning int64
		wantTarget  int64
		wantErr     string
	}{
		{name: "one back", history: []argocd.HistoryEntry{entry(1, revA), entry(2, revB), entry(3, revC)}, back: 1, wantRunning: 3, wantTarget: 2},
		{name: "two back", history: []argocd.HistoryEntry{entry(1, revA), entry(2, revB), entry(3, revC)}, back: 2, wantRunning: 3, wantTarget: 1},
		{name: "history is read by ID, not by place", history: []argocd.HistoryEntry{entry(3, revC), entry(1, revA), entry(2, revB)}, back: 1, wantRunning: 3, wantTarget: 2},
		{
			name:        "the newest entry of a revision deployed twice in a row",
			history:     []argocd.HistoryEntry{entry(1, revA), entry(2, revB), entry(3, revB), entry(4, revC)},
			back:        1,
			wantRunning: 4,
			wantTarget:  3,
		},
		{
			name:        "going back 2 skips a repeat of the revision of going back 1",
			history:     []argocd.HistoryEntry{entry(1, revA), entry(2, revB), entry(3, revB), entry(4, revC)},
			back:        2,
			wantRunning: 4,
			wantTarget:  1,
		},
		{
			name:        "an earlier deploy of the revision that runs is skipped",
			history:     []argocd.HistoryEntry{entry(1, revA), entry(2, revC), entry(3, revB), entry(4, revC)},
			back:        2,
			wantRunning: 4,
			wantTarget:  1,
		},
		{name: "the first entry has ID 0", history: []argocd.HistoryEntry{entry(0, revA), entry(1, revB)}, back: 1, wantRunning: 1, wantTarget: 0},
		{name: "a tag", history: []argocd.HistoryEntry{entry(1, "v1.2.0"), entry(2, "v1.3.0")}, back: 1, wantRunning: 2, wantTarget: 1},
		{
			name:    "no history",
			back:    1,
			wantErr: "Application shop/checkout has no deployment history, so there is nothing to go back to.",
		},
		{
			name:    "only the running revision",
			history: []argocd.HistoryEntry{entry(1, revA), entry(2, revA)},
			back:    1,
			wantErr: "Application shop/checkout has 0 earlier revisions in its history that differ from the one running now, so it cannot go back 1. Argo CD keeps only the last 10 deployments (revisionHistoryLimit), and older ones are gone.",
		},
		{
			name:    "one revision short",
			history: []argocd.HistoryEntry{entry(1, revA), entry(2, revB)},
			back:    2,
			wantErr: "Application shop/checkout has 1 earlier revision in its history that differs from the one running now, so it cannot go back 2.",
		},
		{
			name:    "an entry with several sources",
			history: []argocd.HistoryEntry{{ID: 1, Revisions: []string{revA}, Sources: []argocd.Source{{RepoURL: "x"}}}, entry(2, revB)},
			back:    1,
			wantErr: "Application shop/checkout has several sources",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := plan(appWith(tt.history...), ref, tt.back)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantRunning, got.running.ID)
			require.Equal(t, tt.wantTarget, got.goesBackTo.ID)
		})
	}
}

func TestRevisionOf(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 11, 3, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name  string
		entry argocd.HistoryEntry
		notes string
		want  action.Revision
	}{
		{
			name:  "a commit started by a user",
			entry: argocd.HistoryEntry{ID: 7, Revision: revC, DeployedAt: at, InitiatedBy: argocd.Initiator{Username: "ana"}},
			notes: "Images in the cluster now: shop/checkout:1.4",
			want:  action.Revision{Kind: "Argo CD deployment", ID: "7", Version: "cccc333", DeployedAt: &at, DeployedBy: "ana", Notes: "Images in the cluster now: shop/checkout:1.4"},
		},
		{
			name:  "a tag started by auto-sync",
			entry: argocd.HistoryEntry{ID: 0, Revision: "v1.2.0", DeployedAt: at, InitiatedBy: argocd.Initiator{Automated: true}},
			want:  action.Revision{Kind: "Argo CD deployment", ID: "0", Version: "v1.2.0", DeployedAt: &at, DeployedBy: "automated"},
		},
		{
			name:  "an entry that records neither time nor starter",
			entry: argocd.HistoryEntry{ID: 3, Revision: revA},
			want:  action.Revision{Kind: "Argo CD deployment", ID: "3", Version: "aaaa111"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, revisionOf(tt.entry, tt.notes))
		})
	}
}

func TestImagesNote(t *testing.T) {
	t.Parallel()

	many := make([]string, 12)
	for i := range many {
		many[i] = "img" + string(rune('a'+i))
	}

	tests := []struct {
		name   string
		images []string
		want   string
	}{
		{name: "none"},
		{name: "two", images: []string{"a:1", "b:2"}, want: "Images in the cluster now: a:1, b:2"},
		{name: "more than are shown", images: many, want: "Images in the cluster now: imga, imgb, imgc, imgd, imge, imgf, imgg, imgh, imgi, imgj and 2 more"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			app := argocd.Application{Status: argocd.Status{Summary: argocd.Summary{Images: tt.images}}}
			require.Equal(t, tt.want, imagesNote(&app))
		})
	}
}

func TestSplitVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		version      string
		wantID       int64
		wantRevision string
		wantErr      bool
	}{
		{version: "7:" + revC, wantID: 7, wantRevision: revC},
		{version: "0:v1.2.0", wantID: 0, wantRevision: "v1.2.0"},
		{version: "12:a:b", wantID: 12, wantRevision: "a:b"},
		{version: "7", wantErr: true},
		{version: "x:" + revC, wantErr: true},
		{version: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			t.Parallel()

			id, revision, err := splitVersion(tt.version)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantID, id)
			require.Equal(t, tt.wantRevision, revision)
		})
	}
}

func TestShortRevision(t *testing.T) {
	t.Parallel()

	require.Equal(t, "cccc333", shortRevision(revC))
	require.Equal(t, "cccc333", shortRevision(revC+revC[:24]))
	require.Equal(t, "v1.2.0", shortRevision("v1.2.0"))
	require.Equal(t, "cccc3333", shortRevision("cccc3333"), "an abbreviated hash is left as it is")
	require.Equal(t, revC+"0", shortRevision(revC+"0"), "41 characters are not a hash")
}

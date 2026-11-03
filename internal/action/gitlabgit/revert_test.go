package gitlabgit

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestPlanRevert(t *testing.T) {
	t.Parallel()

	const (
		before = "before\n"
		after  = "after\n"
		other  = "someone else\n"
	)

	sides := func(path string, beforeRef *gitchange.BlobRef) gitchange.SnapshotFiles {
		return gitchange.SnapshotFiles{
			Before: map[string]*gitchange.BlobRef{path: beforeRef},
			After:  map[string]gitchange.AfterEntry{path: {Blob: gitchange.BlobSHA(after), Status: gitchange.FileChanged}},
		}
	}

	beforeRef := &gitchange.BlobRef{Blob: gitchange.BlobSHA(before)}

	tests := []struct {
		name          string
		live          string
		missing       bool
		sides         gitchange.SnapshotFiles
		wantRestores  []Restore
		wantConflicts []string
		wantLive      *gitchange.BlobRef
	}{
		{name: "a file at its earlier content needs nothing", live: before, sides: sides("a.go", beforeRef), wantLive: beforeRef},
		{
			name: "a file as the change left it gets its earlier blob", live: after, sides: sides("a.go", beforeRef),
			wantRestores: []Restore{{Path: "a.go", Blob: gitchange.BlobSHA(before), Exists: true}}, wantLive: &gitchange.BlobRef{Blob: gitchange.BlobSHA(after)},
		},
		{
			name: "a file that someone changed since is a conflict", live: other, sides: sides("a.go", beforeRef),
			wantRestores:  []Restore{{Path: "a.go", Blob: gitchange.BlobSHA(before), Exists: true}},
			wantConflicts: []string{"a.go"}, wantLive: &gitchange.BlobRef{Blob: gitchange.BlobSHA(other)},
		},
		{
			name: "a file the change added is deleted", live: after, sides: sides("a.go", nil),
			wantRestores: []Restore{{Path: "a.go", Exists: true}}, wantLive: &gitchange.BlobRef{Blob: gitchange.BlobSHA(after)},
		},
		{name: "a file the change added and someone deleted needs nothing", missing: true, sides: sides("a.go", nil)},
		{
			name: "a file the change deleted is created again", missing: true, sides: sides("a.go", beforeRef),
			wantRestores: []Restore{{Path: "a.go", Blob: gitchange.BlobSHA(before)}},
		},
		{
			name: "a snapshot with no before side", live: after,
			sides:        gitchange.SnapshotFiles{After: map[string]gitchange.AfterEntry{"a.go": {Blob: gitchange.BlobSHA(after)}}},
			wantRestores: []Restore{{Path: "a.go", Exists: true}}, wantLive: &gitchange.BlobRef{Blob: gitchange.BlobSHA(after)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t)

			if tt.missing {
				b.gitlab.Handle(http.MethodGet, apiPath("repository/files/a.go"), notFound)
				b.gitlab.Handle(http.MethodGet, apiPath("repository/tree"), reply("[]"))
			} else {
				b.gitlab.Handle(http.MethodGet, apiPath("repository/files/a.go"), reply(fileBody("a.go", tt.live)))
			}

			plan, err := PlanRevert(t.Context(), NewReader(b.client, testProject, testBaseSHA), tt.sides, nil)
			require.NoError(t, err)
			require.Equal(t, tt.wantRestores, plan.Restores)
			require.Equal(t, tt.wantConflicts, plan.Conflicts)
			require.Equal(t, map[string]*gitchange.BlobRef{"a.go": tt.wantLive}, plan.Live)
		})
	}
}

func TestPlanRevertGuard(t *testing.T) {
	t.Parallel()

	errRefused := errors.New("refused")

	b := newBed(t)
	sides := gitchange.SnapshotFiles{After: map[string]gitchange.AfterEntry{"a.go": {Blob: "x"}, "b.go": {Blob: "y"}}}

	var seen []string

	_, err := PlanRevert(t.Context(), NewReader(b.client, testProject, testBaseSHA), sides, func(_ context.Context, path string) error {
		seen = append(seen, path)

		return errRefused
	})
	require.ErrorIs(t, err, errRefused)
	require.Equal(t, []string{"a.go"}, seen, "the first refusal ends the plan")
	require.Empty(t, b.gitlab.Requests(), "nothing is read before the guard agrees")
}

func TestRestoreActions(t *testing.T) {
	t.Parallel()

	b := newBed(t)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+gitchange.BlobSHA("one")), reply(blobBody(gitchange.BlobSHA("one"), "one\n")))
	b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+gitchange.BlobSHA("two")), reply(blobBody(gitchange.BlobSHA("two"), "two\n")))

	got, err := RestoreActions(t.Context(), b.client, testProject, []Restore{
		{Path: "added.go", Exists: true},
		{Path: "changed.go", Blob: gitchange.BlobSHA("one"), Exists: true},
		{Path: "deleted.go", Blob: gitchange.BlobSHA("two")},
	})
	require.NoError(t, err)

	one, two := "one\n", "two\n"
	require.Equal(t, []CommitAction{
		{Action: CommitDelete, FilePath: "added.go"},
		{Action: CommitUpdate, FilePath: "changed.go", Content: &one},
		{Action: CommitCreate, FilePath: "deleted.go", Content: &two},
	}, got)
}

func TestRestoreActionsFailsOnABlobItCannotRead(t *testing.T) {
	t.Parallel()

	b := newBed(t)
	b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+gitchange.BlobSHA("one")), notFound)

	_, err := RestoreActions(t.Context(), b.client, testProject, []Restore{{Path: "a.go", Blob: gitchange.BlobSHA("one")}})
	require.ErrorContains(t, err, "read a.go")
}

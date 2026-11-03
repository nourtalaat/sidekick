package gitlabcode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

func TestSnapshotDescribesTheChangeByBlobs(t *testing.T) {
	t.Parallel()

	const (
		newPath = "src/billing/new.go"
		oldPath = "src/billing/old.go"
		newText = "package billing\n"
		oldText = "package billing\n\nvar x = 1\n"
	)

	b := newBed(t, nil)
	b.serveRepo(repo{testPath: regular(testBefore), oldPath: regular(oldText)})

	p := validParams()
	p.Files = []gitchange.FileChange{
		{Path: testPath, Content: text(testAfter)},
		{Path: newPath, Content: text(newText)},
		{Path: oldPath, Delete: true},
	}

	snap, err := b.code.Snapshot(t.Context(), rawParams(t, p))
	require.NoError(t, err)
	require.Equal(t, "gitlab:"+testProject, snap.Target)
	require.Equal(t, testBaseSHA, snap.Version)

	sides, err := gitchange.ReadSnapshot(snap)
	require.NoError(t, err)

	require.Equal(t, map[string]*gitchange.BlobRef{
		testPath: {Blob: blobOf(testBefore)},
		newPath:  nil,
		oldPath:  {Blob: blobOf(oldText)},
	}, sides.Before)

	one, three := 1, 3

	require.Equal(t, map[string]gitchange.AfterEntry{
		testPath: {Blob: blobOf(testAfter), Status: gitchange.FileChanged, LinesAdded: &one, LinesRemoved: &one},
		newPath:  {Blob: blobOf(newText), Status: gitchange.FileAdded, LinesAdded: &one, LinesRemoved: new(0)},
		oldPath:  {Status: gitchange.FileDeleted, LinesRemoved: &three},
	}, sides.After)

	require.NotContains(t, string(snap.Before)+string(snap.After), "return", "a snapshot holds no file contents")
}

func TestSnapshotRefusesWhatTheLimitsDoNotAllow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		repo       repo
		files      []gitchange.FileChange
		limits     func(l *Limits)
		want       string
		wantNoCall bool
	}{
		{name: "a symlink", repo: repo{testPath: node{text: "t", mode: gitlabgit.ModeSymlink}}, files: change(testPath, testAfter), want: "is a symlink"},
		{name: "a submodule", repo: repo{testPath: node{kind: "commit", mode: "160000"}}, files: change(testPath, testAfter), want: "is a submodule"},
		{name: "a directory", repo: repo{testPath: regular(testBefore)}, files: change("src/billing", testAfter), want: "is a directory"},
		{name: "a file that is already as the change wants it", repo: repo{testPath: regular(testAfter)}, files: change(testPath, testAfter), want: "already has this content"},
		{name: "a delete of a file that is not there", repo: repo{testPath: regular(testBefore)}, files: []gitchange.FileChange{{Path: "src/none.go", Delete: true}}, want: "does not exist on the base branch"},
		{name: "a file that is too large at the base", repo: repo{testPath: regular(strings.Repeat("x", gitlabgit.MaxFileBytes+1))}, files: change(testPath, testAfter), want: "is larger than"},
		{name: "a file that is not text at the base", repo: repo{testPath: regular("a\x00b")}, files: change(testPath, testAfter), want: "is not a UTF-8 text file"},
		{name: "more changed lines than the limit", repo: repo{testPath: regular(testBefore)}, files: change(testPath, testAfter), limits: func(l *Limits) { l.MaxChangedLines = 1 }, want: "more than the 1 lines"},
		{name: "a refused path", repo: repo{}, files: change(".github/workflows/ci.yml", "on: push\n"), want: "CI and build automation", wantNoCall: true},
		{name: "a path no glob allows", repo: repo{}, files: change("docs/a.md", "x\n"), want: "matches none of the limits.paths globs", wantNoCall: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			b.serveRepo(tt.repo)

			p := validParams()
			p.Files = tt.files

			_, err := b.code.Snapshot(t.Context(), rawParams(t, p))
			require.ErrorContains(t, err, tt.want)
			require.ErrorIs(t, err, action.ErrLimit, "the approver sees the reason and not a request to try later")

			if tt.wantNoCall {
				require.Empty(t, b.gitlab.Requests())
			}
		})
	}
}

func TestSnapshotLineLimitJustInside(t *testing.T) {
	t.Parallel()

	b := newBed(t, func(l *Limits) { l.MaxChangedLines = 2 })
	b.serveRepo(repo{testPath: regular(testBefore)})

	_, err := b.code.Snapshot(t.Context(), rawParams(t, codeParams()))
	require.NoError(t, err)
}

func TestSnapshotKeepsAServerErrorRetryable(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.gitlab.Handle("GET", apiPath("repository/branches/main"), failed(500, "boom"))

	_, err := b.code.Snapshot(t.Context(), rawParams(t, codeParams()))
	require.Error(t, err)
	require.NotErrorIs(t, err, action.ErrLimit)
}

// change returns one file change with the whole new content.
func change(path, content string) []gitchange.FileChange {
	return []gitchange.FileChange{{Path: path, Content: text(content)}}
}

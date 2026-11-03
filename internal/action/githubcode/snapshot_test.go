package githubcode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

func TestSnapshot(t *testing.T) {
	t.Parallel()

	b := newBed(t, func(l *Limits) { l.AllowDelete = true })
	b.serveBase(repoFiles{
		testGoPath:                regular(testGoBefore),
		"internal/billing/old.go": regular("package billing\n\nvar Old = 1\n"),
	})

	p := validParams()
	p.Files = []gitchange.FileChange{
		{Path: testGoPath, Content: new(testGoAfter)},
		{Path: "internal/billing/new.go", Content: new("package billing\n\nvar New = 2\n")},
		{Path: "internal/billing/old.go", Delete: true},
	}

	snap, err := b.code.Snapshot(t.Context(), rawParams(t, p))
	require.NoError(t, err)

	require.Equal(t, "github:"+testRepo, snap.Target)
	require.Equal(t, testBaseSHA, snap.Version)

	require.JSONEq(t, `{
		"`+testGoPath+`": {"blob": "`+gitchange.BlobSHA(testGoBefore)+`"},
		"internal/billing/new.go": null,
		"internal/billing/old.go": {"blob": "`+gitchange.BlobSHA("package billing\n\nvar Old = 1\n")+`"}
	}`, string(snap.Before))

	require.JSONEq(t, `{
		"`+testGoPath+`": {"blob": "`+gitchange.BlobSHA(testGoAfter)+`", "status": "changed", "linesAdded": 1, "linesRemoved": 1},
		"internal/billing/new.go": {"blob": "`+gitchange.BlobSHA("package billing\n\nvar New = 2\n")+`", "status": "added", "linesAdded": 3, "linesRemoved": 0},
		"internal/billing/old.go": {"status": "deleted", "linesRemoved": 3}
	}`, string(snap.After))

	require.NotContains(t, string(snap.After), "package", "a snapshot holds no file contents")
	require.Len(t, gitchange.FilesOf(snap), 3, "the approver reads each file with its status and line counts")
}

func TestSnapshotOfTooManyLines(t *testing.T) {
	t.Parallel()

	b := newBed(t, func(l *Limits) { l.MaxChangedLines = 2 })
	b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})

	p := validParams()
	p.Files[0].Content = new("package billing\n\nfunc Total() int {\n\treturn 3\n}\n\nfunc Extra() {}\n")

	_, err := b.code.Snapshot(t.Context(), rawParams(t, p))
	require.ErrorIs(t, err, action.ErrLimit)
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		files     repoFiles
		truncated []string
		wantErr   string
	}{
		{name: "a symlink", files: repoFiles{testGoPath: {mode: modeSymlink, typ: typeBlob, content: "../other.go"}}, wantErr: "is a symlink"},
		{name: "a submodule", files: repoFiles{testGoPath: {mode: modeSubmodule, typ: typeCommit, content: "abc"}}, wantErr: "is a submodule"},
		{name: "a directory", files: repoFiles{testGoPath + "/inner.go": regular("x")}, wantErr: "is a directory"},
		{name: "a path through a file", files: repoFiles{"internal/billing": regular("x")}, wantErr: "goes through a path that is a file"},
		{name: "a path that differs only in case", files: repoFiles{"internal/billing/Invoice.go": regular(testGoBefore)}, wantErr: "differs only in case"},
		{name: "a file that is too big", files: repoFiles{testGoPath: regular(strings.Repeat("x", githubgit.MaxFileBytes+1))}, wantErr: "is larger than"},
		{name: "a file that is not text", files: repoFiles{testGoPath: regular("a\x00b")}, wantErr: "is not a UTF-8 text file"},
		{name: "a directory that GitHub lists in part", files: repoFiles{testGoPath: regular(testGoBefore)}, truncated: []string{"internal/billing"}, wantErr: "lists only in part"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.serveBase(tt.files, tt.truncated...)

			_, err := b.code.Snapshot(t.Context(), rawParams(t, validParams()))
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorIs(t, err, action.ErrPermanent)
		})
	}
}

func TestExecuteComparesWithTheSnapshot(t *testing.T) {
	t.Parallel()

	const movedBefore = "package billing\n\nfunc Total() int {\n\treturn 7\n}\n"

	tests := []struct {
		name         string
		live         string
		wantConflict bool
		wantApplied  bool
	}{
		{name: "the file is as it was", live: testGoBefore},
		{name: "someone changed the file", live: movedBefore, wantConflict: true},
		{name: "the file has the new content already", live: testGoAfter, wantApplied: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})

			snap, err := b.code.Snapshot(t.Context(), rawParams(t, validParams()))
			require.NoError(t, err)

			b.serveBase(repoFiles{testGoPath: regular(tt.live)})
			b.expectNoBranch()

			inv := invocation(validParams())
			inv.Snapshot = &snap

			if !tt.wantConflict && !tt.wantApplied {
				b.expectPush()
				b.github.Handle("POST", repoPath("pulls"), replyStatus(201, pullBody(5, true)))
			}

			res, err := b.code.Execute(t.Context(), inv)

			switch {
			case tt.wantConflict:
				require.ErrorIs(t, err, action.ErrConflict)
				require.ErrorContains(t, err, testGoPath)
			case tt.wantApplied:
				require.NoError(t, err)
				require.Contains(t, res.Summary, "on the base branch already")
				require.Nil(t, res.PullRequest)
			default:
				require.NoError(t, err)
				require.Equal(t, &action.PullRequestRef{Number: 5, HeadSHA: testCommitSHA}, res.PullRequest)
			}
		})
	}
}

func TestExecuteRefusesContentThatIsNotTheSnapshots(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.serveBase(repoFiles{testGoPath: regular(testGoBefore)})

	snap, err := b.code.Snapshot(t.Context(), rawParams(t, validParams()))
	require.NoError(t, err)

	other := validParams()
	other.Files[0].Content = new("package billing\n\nfunc Total() int {\n\treturn 9\n}\n")

	inv := invocation(other)
	inv.Snapshot = &snap

	b.expectNoBranch()

	_, err = b.code.Execute(t.Context(), inv)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.ErrorContains(t, err, "is not the content the snapshot describes")
}

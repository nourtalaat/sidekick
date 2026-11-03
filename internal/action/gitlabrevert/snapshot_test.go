package gitlabrevert

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// addFile makes the commit change the file at path from testBefore to
// testAfter, and the head still have what the commit wrote.
func addFile(w *world, path string) {
	w.file(testHead, path, testAfter)
	w.file(testCommit, path, testAfter)
	w.file(testParent, path, testBefore)
}

// diffOf serves the diff of the standard commit with an entry for each path.
func diffOf(w *world, paths ...string) {
	entries := make([]string, len(paths))
	for i, p := range paths {
		entries[i] = diffEntry(p, p, "100644", "100644")
	}

	w.diff(testCommit, entries...)
}

func TestSnapshot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limits  func(l *Limits)
		setup   func(w *world)
		wantErr string
		check   func(t *testing.T, sides gitchange.SnapshotFiles)
	}{
		{name: "a file the commit changed", check: func(t *testing.T, sides gitchange.SnapshotFiles) {
			t.Helper()

			require.Equal(t, map[string]*gitchange.BlobRef{testPath: {Blob: gitchange.BlobSHA(testAfter)}}, sides.Before)

			entry := sides.After[testPath]
			require.Equal(t, gitchange.AfterEntry{Blob: gitchange.BlobSHA(testBefore), Status: gitchange.FileChanged, LinesAdded: new(2), LinesRemoved: new(2)}, entry)
		}},
		{name: "a file the commit added is deleted by the revert", setup: func(w *world) {
			w.diff(testCommit, diffEntry("deploy/new.yaml", "deploy/new.yaml", "0", "100644"))
			w.file(testHead, "deploy/new.yaml", "a: 1\nb: 2\n")
			w.file(testCommit, "deploy/new.yaml", "a: 1\nb: 2\n")
		}, check: func(t *testing.T, sides gitchange.SnapshotFiles) {
			t.Helper()

			require.Equal(t, gitchange.BlobSHA("a: 1\nb: 2\n"), gitchange.BlobOf(sides.Before["deploy/new.yaml"]))
			require.Equal(t, gitchange.AfterEntry{Status: gitchange.FileDeleted, LinesRemoved: new(2)}, sides.After["deploy/new.yaml"])
		}},
		{name: "a file the commit deleted is added back", setup: func(w *world) {
			w.diff(testCommit, diffEntry("deploy/old.yaml", "deploy/old.yaml", "100644", "0"))
			w.file(testParent, "deploy/old.yaml", "a: 1\n")
		}, check: func(t *testing.T, sides gitchange.SnapshotFiles) {
			t.Helper()

			require.Nil(t, sides.Before["deploy/old.yaml"])
			require.Contains(t, sides.Before, "deploy/old.yaml")
			require.Equal(t, gitchange.AfterEntry{Blob: gitchange.BlobSHA("a: 1\n"), Status: gitchange.FileAdded, LinesAdded: new(1), LinesRemoved: new(0)}, sides.After["deploy/old.yaml"])
		}},
		{name: "a renamed file is two files", setup: func(w *world) {
			w.diff(testCommit, diffEntry("deploy/a.yaml", "deploy/b.yaml", "100644", "100644"))
			w.file(testParent, "deploy/a.yaml", testBefore)
			w.file(testCommit, "deploy/b.yaml", testBefore)
			w.file(testHead, "deploy/b.yaml", testBefore)
		}, check: func(t *testing.T, sides gitchange.SnapshotFiles) {
			t.Helper()

			require.Len(t, sides.After, 2)
			require.Equal(t, gitchange.FileAdded, sides.After["deploy/a.yaml"].Status)
			require.Equal(t, gitchange.FileDeleted, sides.After["deploy/b.yaml"].Status)
		}},
		{name: "a file already reverted is left out", setup: func(w *world) {
			diffOf(w, testPath, "deploy/two.yaml")
			addFile(w, "deploy/two.yaml")
			w.file(testHead, testPath, testBefore)
		}, check: func(t *testing.T, sides gitchange.SnapshotFiles) {
			t.Helper()

			require.Len(t, sides.After, 1)
			require.Contains(t, sides.After, "deploy/two.yaml")
		}},
		{name: "every file already reverted", setup: func(w *world) { w.file(testHead, testPath, testBefore) }, wantErr: "back to what it was before already"},
		{name: "a file changed after the commit", setup: func(w *world) { w.file(testHead, testPath, "replicas: 20\n") }, wantErr: `"deploy/values.yaml" changed on the base branch after commit`},
		{name: "a commit that changes no file", setup: func(w *world) { w.diff(testCommit) }, wantErr: "changes no file"},
		{name: "files at the limit", limits: func(l *Limits) { l.MaxFiles = 2 }, setup: func(w *world) {
			diffOf(w, testPath, "deploy/two.yaml")
			addFile(w, "deploy/two.yaml")
		}, check: func(t *testing.T, sides gitchange.SnapshotFiles) {
			t.Helper()

			require.Len(t, sides.After, 2)
		}},
		{name: "files just over the limit", limits: func(l *Limits) { l.MaxFiles = 2 }, setup: func(w *world) {
			diffOf(w, testPath, "deploy/two.yaml", "deploy/three.yaml")
			addFile(w, "deploy/two.yaml")
			addFile(w, "deploy/three.yaml")
		}, wantErr: "more than the 2 files"},
		{name: "a refused path", setup: func(w *world) {
			diffOf(w, ".gitlab-ci.yml")
			addFile(w, ".gitlab-ci.yml")
		}, wantErr: `path ".gitlab-ci.yml" is refused`},
		{name: "a path the operator refuses", limits: func(l *Limits) { l.RefusedPaths = []string{"deploy/**"} }, wantErr: `path "deploy/values.yaml" is refused`},
		{name: "lines at the limit", limits: func(l *Limits) { l.MaxChangedLines = 4 }, check: func(t *testing.T, sides gitchange.SnapshotFiles) {
			t.Helper()

			require.Len(t, sides.After, 1)
		}},
		{name: "lines just over the limit", limits: func(l *Limits) { l.MaxChangedLines = 3 }, wantErr: "more than the 3 lines"},
		{name: "a symbolic link", setup: func(w *world) {
			w.diff(testCommit, diffEntry(testPath, testPath, "120000", "120000"))
		}, wantErr: "symbolic link or a submodule"},
		{name: "a submodule", setup: func(w *world) {
			w.diff(testCommit, diffEntry(testPath, testPath, "160000", "160000"))
		}, wantErr: "symbolic link or a submodule"},
		{name: "a change of mode", setup: func(w *world) {
			w.diff(testCommit, diffEntry(testPath, testPath, "100644", "100755"))
		}, wantErr: "changes its mode"},
		{name: "a file GitLab did not diff in full", setup: func(w *world) {
			w.diff(testCommit, strings.Replace(diffEntry(testPath, testPath, "100644", "100644"), `"collapsed":false`, `"collapsed":true`, 1))
		}, wantErr: "did not diff"},
		{name: "a file that is not text", setup: func(w *world) { w.file(testCommit, testPath, "a\x00b") }, wantErr: "cannot be reverted by this action"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			b.w.standard()

			if tt.setup != nil {
				tt.setup(b.w)
			}

			raw := invocation(commitParams()).Params

			snap, err := b.rv.Snapshot(t.Context(), raw)

			for _, call := range sent(b.w.srv) {
				require.True(t, strings.HasPrefix(call, "GET "), "proposing writes nothing: %s", call)
			}

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit, "the model reads a refusal")

				return
			}

			require.NoError(t, err)
			require.Equal(t, "gitlab:"+testProject, snap.Target)
			require.Equal(t, testHead, snap.Version)

			sides, err := gitchange.ReadSnapshot(snap)
			require.NoError(t, err)
			tt.check(t, sides)
		})
	}
}

func TestSnapshotUsesTheTokenAndReadsAtTheHead(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.standard()

	_, err := b.rv.Snapshot(t.Context(), invocation(commitParams()).Params)
	require.NoError(t, err)

	reqs := b.w.srv.Requests()
	require.NotEmpty(t, reqs)
	require.Equal(t, testTokenValue, reqs[0].Header.Get("PRIVATE-TOKEN"))

	refs := map[string]bool{}

	for _, r := range reqs {
		if r.Path == apiPath("repository/files/"+testPath) {
			refs[r.Query.Get("ref")] = true
		}
	}

	require.Equal(t, map[string]bool{testHead: true, testCommit: true, testParent: true}, refs, "the file is read at the head, in the commit and in its parent")
}

func TestSnapshotOfAMergeRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.standard()
	b.w.mergeRequest(12, mergeRequestBody(12, "merged", false, "feature/x", "", testCommit, ""))

	p := commitParams()
	p.Commit, p.MergeRequest = "", 12

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	snap, err := b.rv.Snapshot(t.Context(), raw)
	require.NoError(t, err)
	require.Contains(t, string(snap.After), gitchange.BlobSHA(testBefore))
}

func TestSnapshotOfAProjectThatIsNotAllowed(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	p := commitParams()
	p.Project = "acme/other/project"

	raw, err := json.Marshal(p)
	require.NoError(t, err)

	_, err = b.rv.Snapshot(t.Context(), raw)
	require.ErrorIs(t, err, action.ErrLimit)
	require.Empty(t, b.w.srv.Requests())
}

func TestSnapshotFailsForGoodWhenGitLabRefuses(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.standard()
	b.w.srv.HandleFunc(http.MethodGet, apiPath("repository/branches/main"), func(rw http.ResponseWriter, _ *http.Request) {
		writeJSON(rw, http.StatusForbidden, `{"message":"403 Forbidden"}`)
	})

	_, err := b.rv.Snapshot(t.Context(), invocation(commitParams()).Params)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrLimit)
}

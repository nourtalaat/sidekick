package githubrevert

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

// prHead is the SHA of the head of the pull requests of the tests. It is
// never read as a commit: only the comparison names it.
var prHead = strings.Repeat("e", 40)

// prSetup builds a repository with one merged pull request, number 7, and
// returns the SHA of its merge commit. It makes main point at it.
type prSetup func(b *bed) wpull

// merged returns a pull request merged 2 hours ago into main.
func merged(mergeSHA string, commits int, files ...string) wpull {
	p := wpull{number: 7, merged: true, mergedAt: testNow.Add(-2 * time.Hour), base: "main", mergeSHA: mergeSHA, commits: commits, headSHA: prHead, title: "Raise the retries"}

	for _, f := range files {
		p.files = append(p.files, wprFile{name: f})
	}

	return p
}

func TestSnapshotByPullRequest(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     prSetup
		wantPaths []string
	}{
		{
			name: "a merge commit is reverted against its first parent",
			setup: func(b *bed) wpull {
				first := b.w.commit(nil, "Initial", testNow.Add(-100*time.Hour), map[string]wfile{readmePath: txt(readmeText), configPath: txt(configBefore)})
				feature := b.w.commit([]string{first}, "Raise the retries", testNow.Add(-3*time.Hour), map[string]wfile{readmePath: txt(readmeText), configPath: txt(configAfter), newFilePath: txt(newFileText)})
				merge := b.w.commit([]string{first, feature}, "Merge pull request #7", testNow.Add(-2*time.Hour), map[string]wfile{readmePath: txt(readmeText), configPath: txt(configAfter), newFilePath: txt(newFileText)})
				b.w.setBranch("main", merge)

				return merged(merge, 4)
			},
			wantPaths: []string{configPath, newFilePath},
		},
		{
			name: "a squash of one commit needs no proof",
			setup: func(b *bed) wpull {
				h := b.standard()
				b.w.setBranch("main", h.bad)

				return merged(h.bad, 1)
			},
			wantPaths: []string{readmePath, configPath, newFilePath},
		},
		{
			name: "a squash of several commits holds the whole change",
			setup: func(b *bed) wpull {
				h := b.standard()
				b.w.setBranch("main", h.bad)
				b.w.compare(h.first, prHead, "ahead", h.first)

				return merged(h.bad, 3, readmePath, configPath, newFilePath)
			},
			wantPaths: []string{readmePath, configPath, newFilePath},
		},
		{
			name: "a pull request that renamed a file lists both names",
			setup: func(b *bed) wpull {
				first := b.w.commit(nil, "Initial", testNow.Add(-100*time.Hour), map[string]wfile{"old.txt": txt("a\n")})
				squash := b.w.commit([]string{first}, "Rename it", testNow.Add(-2*time.Hour), map[string]wfile{"new.txt": txt("a\n")})
				b.w.setBranch("main", squash)
				b.w.compare(first, prHead, "ahead", first)

				p := merged(squash, 2)
				p.files = []wprFile{{name: "new.txt", previous: "old.txt"}}

				return p
			},
			wantPaths: []string{"new.txt", "old.txt"},
		},
		{
			name: "merged just inside maxAge",
			setup: func(b *bed) wpull {
				h := b.standard()
				b.w.setBranch("main", h.bad)

				p := merged(h.bad, 1)
				p.mergedAt = testNow.Add(-72 * time.Hour)

				return p
			},
			wantPaths: []string{readmePath, configPath, newFilePath},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			p := tt.setup(b)
			b.w.pull(p)
			b.w.serve()

			snap, err := b.rev.Snapshot(t.Context(), marshal(t, byPullRequest(p.number)))
			require.NoError(t, err)

			var paths []string
			for _, f := range gitchange.FilesOf(snap) {
				paths = append(paths, f.Path)
			}

			require.Equal(t, tt.wantPaths, paths)
		})
	}
}

func TestSnapshotByPullRequestRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setup     prSetup
		wantErr   string
		wantWhole bool
	}{
		{
			name: "a pull request that is not merged",
			setup: func(b *bed) wpull {
				h := b.standard()
				p := merged(h.bad, 1)
				p.merged = false

				return p
			},
			wantErr: "is not merged",
		},
		{
			name: "a pull request merged into another branch",
			setup: func(b *bed) wpull {
				h := b.standard()
				p := merged(h.bad, 1)
				p.base = "release"

				return p
			},
			wantErr: `was merged into "release"`,
		},
		{
			name: "merged just over maxAge, whatever the date of the commit",
			setup: func(b *bed) wpull {
				h := b.standard()
				p := merged(h.bad, 1)
				p.mergedAt = testNow.Add(-72*time.Hour - time.Minute)

				return p
			},
			wantErr: "longer than limits.maxAge",
		},
		{
			name: "a pull request that is a revert of this action",
			setup: func(b *bed) wpull {
				h := b.standard()
				p := merged(h.bad, 1)
				p.body = "Reverts commit abc.\n\nSidekick-Revert: " + testProposalID

				return p
			},
			wantErr: "is a revert made by this action",
		},
		{
			name: "a merge commit that is a revert of this action",
			setup: func(b *bed) wpull {
				first := b.w.commit(nil, "Initial", testNow.Add(-100*time.Hour), map[string]wfile{"a.txt": txt("a\n")})
				squash := b.w.commit([]string{first}, "Undo (#7)\n\nSidekick-Revert: "+testProposalID, testNow.Add(-2*time.Hour), map[string]wfile{"a.txt": txt("b\n")})
				b.w.setBranch("main", squash)

				return merged(squash, 1)
			},
			wantErr: "is a revert made by this action",
		},
		{
			name: "a rebase whose commits touch different files",
			setup: func(b *bed) wpull {
				first := b.w.commit(nil, "Initial", testNow.Add(-100*time.Hour), map[string]wfile{configPath: txt(configBefore)})
				one := b.w.commit([]string{first}, "Add the file", testNow.Add(-2*time.Hour), map[string]wfile{configPath: txt(configBefore), newFilePath: txt(newFileText)})
				two := b.w.commit([]string{one}, "Raise the retries", testNow.Add(-2*time.Hour), map[string]wfile{configPath: txt(configAfter), newFilePath: txt(newFileText)})
				b.w.setBranch("main", two)

				return merged(two, 2, configPath, newFilePath)
			},
			wantErr:   "name the commits with commit instead",
			wantWhole: true,
		},
		{
			name: "a rebase whose commits touch the same file twice",
			setup: func(b *bed) wpull {
				first := b.w.commit(nil, "Initial", testNow.Add(-100*time.Hour), map[string]wfile{configPath: txt(configBefore)})
				one := b.w.commit([]string{first}, "Raise the retries", testNow.Add(-2*time.Hour), map[string]wfile{configPath: txt("retries: 10\n")})
				two := b.w.commit([]string{one}, "Raise them more", testNow.Add(-2*time.Hour), map[string]wfile{configPath: txt(configAfter)})
				b.w.setBranch("main", two)
				b.w.compare(one, prHead, "ahead", first)

				return merged(two, 2, configPath)
			},
			wantErr:   "name the commits with commit instead",
			wantWhole: true,
		},
		{
			name: "a squash whose file changed on the base since the pull request began",
			setup: func(b *bed) wpull {
				first := b.w.commit(nil, "Initial", testNow.Add(-100*time.Hour), map[string]wfile{configPath: txt(configBefore)})
				other := b.w.commit([]string{first}, "Someone tunes it", testNow.Add(-5*time.Hour), map[string]wfile{configPath: txt("retries: 4\n")})
				squash := b.w.commit([]string{other}, "Raise the retries (#7)", testNow.Add(-2*time.Hour), map[string]wfile{configPath: txt("retries: 40\n")})
				b.w.setBranch("main", squash)
				b.w.compare(other, prHead, "ahead", first)

				return merged(squash, 3, configPath)
			},
			wantErr:   "name the commits with commit instead",
			wantWhole: true,
		},
		{
			name: "a pull request that lists more files than maxFiles",
			setup: func(b *bed) wpull {
				h := b.standard()
				b.w.setBranch("main", h.bad)

				return merged(h.bad, 3, "a", "b", "c", "d", "e", "f")
			},
			wantErr: "more than the 5 files",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			p := tt.setup(b)
			b.w.pull(p)
			b.w.serve()

			_, err := b.rev.Snapshot(t.Context(), marshal(t, byPullRequest(p.number)))
			require.ErrorContains(t, err, tt.wantErr)
			require.Equal(t, tt.wantWhole, errors.Is(err, errNotWholeChange))
			require.ErrorIs(t, err, action.ErrLimit, "the model reads a refusal, not a request to try later")
		})
	}
}

func TestSnapshotOfAPullRequestThatDoesNotExist(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.standard()
	b.w.gh.HandleFunc(http.MethodGet, repoPath("pulls/99"), func(rw http.ResponseWriter, _ *http.Request) {
		write(rw, http.StatusNotFound, map[string]string{"message": "Not Found"})
	})

	b.w.serve()

	_, err := b.rev.Snapshot(t.Context(), marshal(t, byPullRequest(99)))
	require.ErrorIs(t, err, action.ErrLimit, "the model reads that it named a pull request that is not there")
	require.ErrorContains(t, err, "pull request #99 was not found")
}

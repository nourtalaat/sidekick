package githubrevert

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

func TestSnapshotByCommit(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)

	snap, err := b.rev.Snapshot(t.Context(), marshal(t, params))
	require.NoError(t, err)

	require.Equal(t, "github:"+testRepo, snap.Target)
	require.Equal(t, h.later, snap.Version)

	require.JSONEq(t, fmt.Sprintf(`{
		%q: null,
		%q: {"blob": %q},
		%q: {"blob": %q}
	}`, readmePath, configPath, blobOf(configAfter), newFilePath, blobOf(newFileText)), string(snap.Before))

	require.JSONEq(t, fmt.Sprintf(`{
		%q: {"blob": %q, "status": "added", "linesAdded": 1, "linesRemoved": 0},
		%q: {"blob": %q, "status": "changed", "linesAdded": 1, "linesRemoved": 1},
		%q: {"status": "deleted", "linesRemoved": 1}
	}`, readmePath, blobOf(readmeText), configPath, blobOf(configBefore), newFilePath), string(snap.After))

	require.Equal(t, []action.ProposalFile{
		{Path: readmePath, Status: gitchange.FileAdded, LinesAdded: 1},
		{Path: configPath, Status: gitchange.FileChanged, LinesAdded: 1, LinesRemoved: 1},
		{Path: newFilePath, Status: gitchange.FileDeleted, LinesRemoved: 1},
	}, gitchange.FilesOf(snap))
}

func TestSnapshotLeavesOutFilesAlreadyBack(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()

	// Someone put the readme back already; the config and the new file are as
	// the bad commit left them.
	head := b.w.commit([]string{h.later}, "Restore the readme", testNow, map[string]wfile{
		readmePath: txt(readmeText), configPath: txt(configAfter), limitsPath: txt("max: 20\n"), newFilePath: txt(newFileText),
	})
	b.w.setBranch("main", head)
	b.onMain(h.bad)
	b.w.serve()

	snap := b.snapshotOf(t, byCommit(h.bad))

	require.JSONEq(t, fmt.Sprintf(`{%q: {"blob": %q}, %q: {"blob": %q}}`, configPath, blobOf(configAfter), newFilePath, blobOf(newFileText)), string(snap.Before))
	require.Equal(t, head, snap.Version)
}

func TestSnapshotKeepsTheModeOfAnExecutable(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"run.sh": {content: "echo a\n", mode: modeExecutable}})
	bad := b.w.commit([]string{first}, "Change the script", testNow.Add(-time.Hour), map[string]wfile{"run.sh": {content: "echo b\n", mode: modeExecutable}})
	b.w.setBranch("main", bad)
	b.onMain(bad)
	b.w.serve()

	snap := b.snapshotOf(t, byCommit(bad))
	require.JSONEq(t, fmt.Sprintf(`{"run.sh": {"blob": %q, "status": "changed", "linesAdded": 1, "linesRemoved": 1}}`, blobOf("echo a\n")), string(snap.After))
}

func TestSnapshotRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limits  func(l *Limits)
		setup   func(b *bed) Params
		wantErr string
	}{
		{
			name: "a file changed since the commit",
			setup: func(b *bed) Params {
				h := b.standard()
				head := b.w.commit([]string{h.later}, "Tune the retries", testNow, map[string]wfile{
					configPath: txt("retries: 4\n"), limitsPath: txt("max: 20\n"), newFilePath: txt(newFileText),
				})
				b.w.setBranch("main", head)
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
			wantErr: `"svc/config.yaml" changed on the base branch after commit`,
		},
		{
			name: "every file is back already",
			setup: func(b *bed) Params {
				h := b.standard()
				head := b.w.commit([]string{h.later}, "Undo by hand", testNow, map[string]wfile{
					readmePath: txt(readmeText), configPath: txt(configBefore), limitsPath: txt("max: 20\n"),
				})
				b.w.setBranch("main", head)
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
			wantErr: "back to what it was before already",
		},
		{
			name: "landed just over maxAge ago",
			setup: func(b *bed) Params {
				h := b.standardAged(72*time.Hour + time.Minute)
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
			wantErr: "longer than limits.maxAge (72h0m0s)",
		},
		{
			name:   "landed just inside a smaller maxAge",
			limits: func(l *Limits) { l.MaxAge = 2 * time.Hour },
			setup: func(b *bed) Params {
				h := b.standardAged(2 * time.Hour)
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
		},
		{
			name:   "landed just over a smaller maxAge",
			limits: func(l *Limits) { l.MaxAge = 2 * time.Hour },
			setup: func(b *bed) Params {
				h := b.standardAged(2*time.Hour + time.Minute)
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
			wantErr: "longer than limits.maxAge (2h0m0s)",
		},
		{
			name: "a commit that is not on the base branch",
			setup: func(b *bed) Params {
				h := b.standard()
				b.w.compare(h.bad, "main", "diverged", h.first)

				return byCommit(h.bad)
			},
			wantErr: "is not on the base branch",
		},
		{
			name: "a path of the built-in refused list",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{".github/workflows/ci.yml": txt("on: push\n")})
				bad := b.w.commit([]string{first}, "Change CI", testNow.Add(-time.Hour), map[string]wfile{".github/workflows/ci.yml": txt("on: pull_request\n")})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: "built-in refused path",
		},
		{
			name:   "a path the operator refuses",
			limits: func(l *Limits) { l.RefusedPaths = []string{"svc/**"} },
			setup: func(b *bed) Params {
				h := b.standard()
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
			wantErr: `limits.refusedPaths "svc/**"`,
		},
		{
			name:   "one file over maxFiles",
			limits: func(l *Limits) { l.MaxFiles = 2 },
			setup: func(b *bed) Params {
				h := b.standard()
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
			wantErr: "more than the 2 files",
		},
		{
			name:   "exactly maxFiles",
			limits: func(l *Limits) { l.MaxFiles = 3 },
			setup: func(b *bed) Params {
				h := b.standard()
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
		},
		{
			name:   "one line over maxChangedLines",
			limits: func(l *Limits) { l.MaxChangedLines = 3 },
			setup: func(b *bed) Params {
				h := b.standard()
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
			wantErr: "more than the 3 lines",
		},
		{
			name:   "exactly maxChangedLines",
			limits: func(l *Limits) { l.MaxChangedLines = 4 },
			setup: func(b *bed) Params {
				h := b.standard()
				b.onMain(h.bad)

				return byCommit(h.bad)
			},
		},
		{
			name: "a commit that is a revert of this action",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"a.txt": txt("a\n")})
				bad := b.w.commit([]string{first}, "Revert \"x\"\n\nThis reverts commit abc.\n\nSidekick-Revert: "+testProposalID+"\n", testNow.Add(-time.Hour), map[string]wfile{"a.txt": txt("b\n")})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: "is a revert made by this action",
		},
		{
			name: "a commit with no parent",
			setup: func(b *bed) Params {
				root := b.w.commit(nil, "Initial", testNow.Add(-time.Hour), map[string]wfile{"a.txt": txt("a\n")})
				b.w.setBranch("main", root)
				b.onMain(root)

				return byCommit(root)
			},
			wantErr: "has no parent commit",
		},
		{
			name: "a commit with three parents",
			setup: func(b *bed) Params {
				a := b.w.commit(nil, "A", testNow.Add(-5*time.Hour), map[string]wfile{"a.txt": txt("a\n")})
				octopus := b.w.commit([]string{a, a, a}, "Octopus", testNow.Add(-time.Hour), map[string]wfile{"a.txt": txt("b\n")})
				b.w.setBranch("main", octopus)
				b.onMain(octopus)

				return byCommit(octopus)
			},
			wantErr: "has 3 parents",
		},
		{
			name: "a symbolic link",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"link": {content: "a", mode: modeSymlink}})
				bad := b.w.commit([]string{first}, "Move the link", testNow.Add(-time.Hour), map[string]wfile{"link": {content: "b", mode: modeSymlink}})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: "is a symbolic link",
		},
		{
			name: "a submodule",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"vendor/lib": {content: "one", kind: "commit"}})
				bad := b.w.commit([]string{first}, "Bump the submodule", testNow.Add(-time.Hour), map[string]wfile{"vendor/lib": {content: "two", kind: "commit"}})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: "is a submodule",
		},
		{
			name: "a change of the file mode",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"run.sh": txt("echo\n")})
				bad := b.w.commit([]string{first}, "Make it executable", testNow.Add(-time.Hour), map[string]wfile{"run.sh": {content: "echo\n", mode: modeExecutable}})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: "another file mode",
		},
		{
			name: "a file that became a directory",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"conf": txt("a\n")})
				bad := b.w.commit([]string{first}, "Split it", testNow.Add(-time.Hour), map[string]wfile{"conf/a.txt": txt("a\n")})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: "became a directory",
		},
		{
			name: "a file that is not text",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"logo.png": txt("a\x00b")})
				bad := b.w.commit([]string{first}, "New logo", testNow.Add(-time.Hour), map[string]wfile{"logo.png": txt("c\x00d")})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: `"logo.png": blob`,
		},
		{
			name: "a file over the size limit",
			setup: func(b *bed) Params {
				first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"big.txt": txt("a\n")})
				bad := b.w.commit([]string{first}, "Grow", testNow.Add(-time.Hour), map[string]wfile{"big.txt": txt(strings.Repeat("x", githubgit.MaxFileBytes+1))})
				b.w.setBranch("main", bad)
				b.onMain(bad)

				return byCommit(bad)
			},
			wantErr: "is larger than",
		},
		{
			name: "a directory too large for GitHub to list",
			setup: func(b *bed) Params {
				h := b.standard()
				b.onMain(h.bad)
				b.w.mu.Lock()
				b.w.truncated[b.w.commits[h.bad].tree] = true
				b.w.mu.Unlock()

				return byCommit(h.bad)
			},
			wantErr: "too many entries",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)
			params := tt.setup(b)
			b.w.serve()

			_, err := b.rev.Snapshot(t.Context(), marshal(t, params))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit, "the model reads a refusal, not a request to try later")
		})
	}
}

func TestSnapshotRefusesWhatNeedsNoNetwork(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.w.serve()

	tests := []struct {
		name   string
		change func(p *Params)
		want   string
	}{
		{name: "another repository", change: func(p *Params) { p.Repo = "acme/other" }, want: "is not one of limits.repos"},
		{name: "another base branch", change: func(p *Params) { p.BaseBranch = "dev" }, want: "is not the allowed base branch"},
		{name: "both a commit and a pull request", change: func(p *Params) { p.PullRequest = 4 }, want: "either commit or pullRequest"},
	}

	t.Cleanup(func() {
		require.Empty(t, sent(b.w.gh), "the checks need no call to GitHub")
	})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := byCommit(h.bad)
			tt.change(&p)

			_, err := b.rev.Snapshot(t.Context(), marshal(t, p))
			require.ErrorContains(t, err, tt.want)
		})
	}
}

package githubrevert

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// count returns how many requests the fake GitHub got for the call.
func count(b *bed, call string) int {
	n := 0

	for _, c := range sent(b.w.gh) {
		if c == call {
			n++
		}
	}

	return n
}

// writes returns the calls of the fake GitHub that change something.
func writes(b *bed) []string {
	var out []string

	for _, c := range sent(b.w.gh) {
		if len(c) > 3 && c[:3] != "GET" {
			out = append(out, c)
		}
	}

	return out
}

func TestExecuteOpensADraftPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)
	params.Body = "It doubled the load. cc @octocat, fixes #9"
	snap := b.snapshotOf(t, params)

	inv := invocation(t, params, &snap)

	res, err := b.rev.Execute(t.Context(), inv)
	require.NoError(t, err)
	require.Equal(t, "Opened draft pull request acme/api#100", res.Summary)
	require.Equal(t, []action.Link{{Title: "acme/api#100", URL: "https://github.com/acme/api/pull/100"}}, res.Links)
	require.Equal(t, &action.PullRequestRef{Number: createdNumber, HeadSHA: testCommitSHA}, res.PullRequest)

	require.Equal(t, []string{
		"POST /repos/acme/api/git/trees",
		"POST /repos/acme/api/git/commits",
		"POST /repos/acme/api/git/refs",
		"POST /repos/acme/api/pulls",
	}, writes(b))

	b.w.mu.Lock()
	headTree := b.w.commits[h.later].tree
	b.w.mu.Unlock()

	tree := bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, headTree, tree["base_tree"])
	require.Equal(t, []any{
		map[string]any{"path": readmePath, "mode": "100644", "type": "blob", "sha": blobOf(readmeText)},
		map[string]any{"path": configPath, "mode": "100644", "type": "blob", "sha": blobOf(configBefore)},
		map[string]any{"path": newFilePath, "sha": nil},
	}, tree["tree"])

	commit := bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/commits"))
	require.Equal(t, testTreeSHA, commit["tree"])
	require.Equal(t, []any{h.later}, commit["parents"])
	require.Equal(t, "Revert \"Raise the retries\"\n\nThis reverts commit "+h.bad+".\n\nSidekick-Revert: "+testProposalID+"\n", commit["message"])

	require.Equal(t, map[string]any{"ref": "refs/heads/" + testBranch, "sha": testCommitSHA}, bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/refs")))

	pull := bodyOf(t, b.w.gh, http.MethodPost, repoPath("pulls"))
	require.ElementsMatch(t, []string{"title", "head", "base", "body", "draft"}, keys(pull), "no reviewer, assignee, merge or auto-merge field")
	require.Equal(t, testBranch, pull["head"])
	require.Equal(t, "main", pull["base"])
	require.Equal(t, "Revert the retry change", pull["title"])
	requireDraft(t, pull)

	body, ok := pull["body"].(string)
	require.True(t, ok)
	require.Contains(t, body, "cc `@octocat`, fixes `#9`", "mentions and closing keywords are made harmless")
	require.Contains(t, body, "Reverts commit "+h.bad+".")
	require.Contains(t, body, "Sidekick-Revert: "+testProposalID)
	require.Contains(t, body, "Opened by Sidekick for proposal `"+testProposalID+"`.")
	require.Contains(t, body, "https://sidekick.example.com")
}

func TestExecuteOfAPullRequestNamesItInTheMessage(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.w.setBranch("main", h.bad)
	b.w.pull(merged(h.bad, 1))
	b.w.serve()

	params := byPullRequest(7)
	snap := b.snapshotOf(t, params)

	_, err := b.rev.Execute(t.Context(), invocation(t, params, &snap))
	require.NoError(t, err)

	commit := bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/commits"))
	require.Equal(t, "Revert \"Raise the retries\"\n\nThis reverts pull request #7, merge commit "+h.bad+".\n\nSidekick-Revert: "+testProposalID+"\n", commit["message"])

	pull := bodyOf(t, b.w.gh, http.MethodPost, repoPath("pulls"))
	require.Contains(t, pull["body"], "Reverts pull request #7.")
}

func TestExecuteWritesTheModeOfTheParent(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	first := b.w.commit(nil, "Initial", testNow.Add(-5*time.Hour), map[string]wfile{"run.sh": {content: "echo a\n", mode: modeExecutable}})
	bad := b.w.commit([]string{first}, "Change the script", testNow.Add(-time.Hour), map[string]wfile{"run.sh": {content: "echo b\n", mode: modeExecutable}})
	b.w.setBranch("main", bad)
	b.onMain(bad)
	b.w.serve()

	params := byCommit(bad)
	snap := b.snapshotOf(t, params)

	_, err := b.rev.Execute(t.Context(), invocation(t, params, &snap))
	require.NoError(t, err)

	tree := bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, []any{map[string]any{"path": "run.sh", "mode": "100755", "type": "blob", "sha": blobOf("echo a\n")}}, tree["tree"])
}

func TestExecuteRepeatMakesOneBranchAndOnePullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)
	snap := b.snapshotOf(t, params)
	inv := invocation(t, params, &snap)

	first, err := b.rev.Execute(t.Context(), inv)
	require.NoError(t, err)

	second, err := b.rev.Execute(t.Context(), inv)
	require.NoError(t, err)

	require.Equal(t, "Found the already opened draft pull request acme/api#100", second.Summary)
	require.Equal(t, first.PullRequest, second.PullRequest)
	require.Equal(t, first.Links, second.Links)

	for _, call := range []string{"POST /repos/acme/api/git/trees", "POST /repos/acme/api/git/commits", "POST /repos/acme/api/git/refs", "POST /repos/acme/api/pulls"} {
		require.Equal(t, 1, count(b, call), call)
	}
}

func TestExecuteAfterACrashBetweenTheBranchAndThePullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.setBranch(testBranch, testCommitSHA)
	b.w.serve()

	// The branch is there and has no pull request, so nothing needs a
	// snapshot or a read of the repository.
	res, err := b.rev.Execute(t.Context(), invocation(t, byCommit(h.bad), nil))
	require.NoError(t, err)
	require.Equal(t, "Opened draft pull request acme/api#100", res.Summary)
	require.Equal(t, []string{"POST /repos/acme/api/pulls"}, writes(b))
}

func TestExecuteReportsAConflictForAFileThatChangedSinceTheProposal(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)
	snap := b.snapshotOf(t, params)

	head := b.w.commit([]string{h.later}, "Tune the retries", testNow, map[string]wfile{
		configPath: txt("retries: 4\n"), limitsPath: txt("max: 20\n"), newFilePath: txt(newFileText),
	})
	b.w.setBranch("main", head)
	b.w.serve()

	_, err := b.rev.Execute(t.Context(), invocation(t, params, &snap))
	require.ErrorIs(t, err, action.ErrConflict)
	require.ErrorContains(t, err, `"svc/config.yaml" changed on the base branch after the proposal was made`)
	require.Empty(t, writes(b))
}

func TestExecuteLeavesOutFilesThatAreBackAlready(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)
	snap := b.snapshotOf(t, params)

	// Meanwhile someone put the config back by hand.
	head := b.w.commit([]string{h.later}, "Put the config back", testNow, map[string]wfile{
		configPath: txt(configBefore), limitsPath: txt("max: 20\n"), newFilePath: txt(newFileText),
	})
	b.w.setBranch("main", head)
	b.w.serve()

	_, err := b.rev.Execute(t.Context(), invocation(t, params, &snap))
	require.NoError(t, err)

	tree := bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/trees"))
	require.Len(t, tree["tree"], 2, "the config is not written again")
}

func TestExecuteWhenEveryFileIsBackAlready(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)
	snap := b.snapshotOf(t, params)

	head := b.w.commit([]string{h.later}, "Revert by hand", testNow, map[string]wfile{
		readmePath: txt(readmeText), configPath: txt(configBefore), limitsPath: txt("max: 20\n"),
	})
	b.w.setBranch("main", head)
	b.w.serve()

	res, err := b.rev.Execute(t.Context(), invocation(t, params, &snap))
	require.NoError(t, err)
	require.Equal(t, "The change is reverted on the base branch already, so no pull request was opened.", res.Summary)
	require.Nil(t, res.PullRequest)
	require.Empty(t, writes(b))
}

func TestExecuteChecksAgainWhenItRuns(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)
	snap := b.snapshotOf(t, params)

	b.rev.now = func() time.Time { return testNow.Add(72 * time.Hour) }

	_, err := b.rev.Execute(t.Context(), invocation(t, params, &snap))
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "longer than limits.maxAge")
	require.Empty(t, writes(b))
}

func TestExecuteRefusals(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	params := byCommit(h.bad)
	snap := b.snapshotOf(t, params)

	tests := []struct {
		name    string
		change  func(inv *action.Invocation)
		wantErr string
		wantIs  error
	}{
		{name: "no snapshot", change: func(inv *action.Invocation) { inv.Snapshot = nil }, wantErr: "no snapshot", wantIs: action.ErrPermanent},
		{name: "no branch key", change: func(inv *action.Invocation) { inv.IdempotencyKey = "other" }, wantErr: "idempotency key", wantIs: action.ErrPermanent},
		{name: "a base branch outside the limits", change: func(inv *action.Invocation) {
			p := params
			p.BaseBranch = "dev"
			inv.Params = marshal(t, p)
		}, wantErr: "is not the allowed base branch", wantIs: action.ErrLimit},
		{name: "a snapshot of another commit", change: func(inv *action.Invocation) {
			other := snap
			other.After = []byte(`{"svc/config.yaml": {"blob": "` + blobOf("else\n") + `", "status": "changed"}}`)
			other.Before = []byte(`{"svc/config.yaml": {"blob": "` + blobOf(configAfter) + `"}}`)
			inv.Snapshot = &other
		}, wantErr: "is not what commit", wantIs: action.ErrPermanent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inv := invocation(t, params, &snap)
			tt.change(&inv)

			_, err := b.rev.Execute(t.Context(), inv)
			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, tt.wantIs)
		})
	}
}

// keys returns the keys of the map.
func keys(m map[string]any) []string {
	out := make([]string, 0, len(m))

	for k := range m {
		out = append(out, k)
	}

	return out
}

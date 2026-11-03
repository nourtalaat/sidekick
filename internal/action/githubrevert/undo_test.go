package githubrevert

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// The number of the pull request the revert opened, which the undo tests
// start from, and the branch an undo makes.
const originalNumber = 99

var testUndoBranch = "sidekick/" + testUndoID

// undoInvocation returns the invocation that undoes the proposal of params.
func undoInvocation(t *testing.T, params Params) action.Invocation {
	t.Helper()

	inv := invocation(t, params, nil)
	inv.ProposalID = testUndoID
	inv.IdempotencyKey = action.IdempotencyKey(testUndoID)
	inv.UndoOf = testProposalID

	return inv
}

// closePull makes the fake GitHub close the pull request with the number.
func closePull(b *bed, number int) {
	b.w.gh.HandleFunc(http.MethodPatch, repoPath("pulls/"+strconv.Itoa(number)), func(rw http.ResponseWriter, _ *http.Request) {
		write(rw, http.StatusOK, map[string]any{
			"number": number, "state": "closed", "html_url": "https://github.com/acme/api/pull/" + strconv.Itoa(number), "closed_at": "2026-11-03T11:00:00Z",
		})
	})
}

// afterMergedRevert builds the repository as it is after the revert of the
// standard bad commit was merged: the readme is back, the config is as it was
// and the new file is gone. It returns the snapshot of the revert, taken
// before it merged.
func afterMergedRevert(t *testing.T, b *bed) (history, action.Snapshot) {
	t.Helper()

	h := b.standard()
	b.onMain(h.bad)
	b.w.serve()

	snap := b.snapshotOf(t, byCommit(h.bad))

	head := b.w.commit([]string{h.later}, "Merge the revert", testNow, map[string]wfile{
		readmePath: txt(readmeText), configPath: txt(configBefore), limitsPath: txt("max: 20\n"),
	})
	b.w.setBranch("main", head)
	b.w.ownPull(originalNumber, testBranch, "closed", true)
	b.w.serve()

	return h, snap
}

func TestUndoClosesAnOpenPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.ownPull(originalNumber, testBranch, "open", false)
	closePull(b, originalNumber)
	b.w.gh.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})
	b.w.serve()

	res, err := b.rev.Undo(t.Context(), undoInvocation(t, byCommit(testCommitSHA)), action.Snapshot{}, false)
	require.NoError(t, err)
	require.Equal(t, "Closed the pull request acme/api#99 and deleted its branch.", res.Summary)
	require.Equal(t, []action.Link{{Title: "acme/api#99", URL: "https://github.com/acme/api/pull/99"}}, res.Links)
	require.Equal(t, map[string]any{"state": "closed"}, bodyOf(t, b.w.gh, http.MethodPatch, repoPath("pulls/99")))
}

func TestUndoOfAClosedPullRequestOnlyDeletesTheBranch(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.ownPull(originalNumber, testBranch, "closed", false)
	b.w.gh.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakehttp.Response{
		Status: http.StatusUnprocessableEntity, Headers: jsonHeaders, Body: []byte(`{"message":"Reference does not exist"}`),
	})
	b.w.serve()

	res, err := b.rev.Undo(t.Context(), undoInvocation(t, byCommit(testCommitSHA)), action.Snapshot{}, false)
	require.NoError(t, err, "a branch that is gone already is not an error")
	require.Equal(t, "The pull request acme/api#99 was closed already; its branch is deleted.", res.Summary)
	require.Zero(t, count(b, "PATCH /repos/acme/api/pulls/99"), "a closed pull request is not closed again")
}

func TestUndoPutsBackWhatTheCommitWrote(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h, snap := afterMergedRevert(t, b)

	params := byCommit(h.bad)
	res, err := b.rev.Undo(t.Context(), undoInvocation(t, params), snap, false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Opened the draft pull request acme/api#100, which puts back what the revert took out. It still needs review and merging.")
	require.Equal(t, &action.PullRequestRef{Number: createdNumber, HeadSHA: testCommitSHA}, res.PullRequest)

	tree := bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, []any{
		map[string]any{"path": readmePath, "sha": nil},
		map[string]any{"path": configPath, "mode": "100644", "type": "blob", "sha": blobOf(configAfter)},
		map[string]any{"path": newFilePath, "mode": "100644", "type": "blob", "sha": blobOf(newFileText)},
	}, tree["tree"])

	commit := bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/commits"))
	require.Equal(t, "Undo the revert of commit "+h.bad[:shortSHA]+"\n\nSidekick proposal "+testUndoID+", which undoes "+testProposalID+".\n\nSidekick-Revert: "+testUndoID+"\n", commit["message"])
	require.Equal(t, map[string]any{"ref": "refs/heads/" + testUndoBranch, "sha": testCommitSHA}, bodyOf(t, b.w.gh, http.MethodPost, repoPath("git/refs")))

	pull := bodyOf(t, b.w.gh, http.MethodPost, repoPath("pulls"))
	require.Equal(t, testUndoBranch, pull["head"])
	require.Equal(t, "Undo: Revert the retry change", pull["title"])
	requireDraft(t, pull)
	require.ElementsMatch(t, []string{"title", "head", "base", "body", "draft"}, keys(pull))
	require.Contains(t, pull["body"], "Sidekick-Revert: "+testUndoID)
}

func TestUndoIsNotRefusedForTheAgeOfTheCommit(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h, snap := afterMergedRevert(t, b)

	// By now the commit is far older than limits.maxAge, which only limits a
	// new revert.
	b.rev.now = func() time.Time { return testNow.Add(200 * 24 * time.Hour) }

	_, err := b.rev.Undo(t.Context(), undoInvocation(t, byCommit(h.bad)), snap, false)
	require.NoError(t, err)
}

func TestUndoReportsAConflictAndForceOverwrites(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h, snap := afterMergedRevert(t, b)

	// Someone changed the config after the revert merged.
	head := b.w.commit([]string{}, "Tune the retries", testNow, map[string]wfile{
		readmePath: txt(readmeText), configPath: txt("retries: 4\n"), limitsPath: txt("max: 20\n"),
	})
	b.w.setBranch("main", head)
	b.w.serve()

	params := byCommit(h.bad)

	_, err := b.rev.Undo(t.Context(), undoInvocation(t, params), snap, false)
	require.ErrorIs(t, err, action.ErrConflict)

	conflict, ok := errors.AsType[*action.ConflictError](err)
	require.True(t, ok)
	require.JSONEq(t, `{"README.md": {"blob": "`+blobOf(readmeText)+`"}, "svc/config.yaml": {"blob": "`+blobOf("retries: 4\n")+`"}, "svc/new.go": null}`, string(conflict.Live))
	require.Empty(t, writes(b), "nothing is written without force")

	res, err := b.rev.Undo(t.Context(), undoInvocation(t, params), snap, true)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Opened the draft pull request acme/api#100")
	require.Equal(t, []string{"POST /repos/acme/api/git/trees", "POST /repos/acme/api/git/commits", "POST /repos/acme/api/git/refs", "POST /repos/acme/api/pulls"}, writes(b))
}

func TestUndoRepeatMakesOneBranchAndOnePullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h, snap := afterMergedRevert(t, b)

	inv := undoInvocation(t, byCommit(h.bad))

	first, err := b.rev.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)

	second, err := b.rev.Undo(t.Context(), inv, snap, false)
	require.NoError(t, err)
	require.Equal(t, first.PullRequest, second.PullRequest)

	for _, call := range []string{"POST /repos/acme/api/git/trees", "POST /repos/acme/api/git/commits", "POST /repos/acme/api/git/refs", "POST /repos/acme/api/pulls"} {
		require.Equal(t, 1, count(b, call), call)
	}
}

func TestUndoWhenTheCommitIsBackAlready(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	h, snap := afterMergedRevert(t, b)

	head := b.w.commit([]string{h.later}, "Put the bad commit back by hand", testNow, map[string]wfile{
		configPath: txt(configAfter), limitsPath: txt("max: 20\n"), newFilePath: txt(newFileText),
	})
	b.w.setBranch("main", head)
	b.w.serve()

	res, err := b.rev.Undo(t.Context(), undoInvocation(t, byCommit(h.bad)), snap, false)
	require.NoError(t, err)
	require.Equal(t, "Nothing to put back: the files are as the commit left them.", res.Summary)
	require.Empty(t, writes(b))
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	t.Run("no pull request was made", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.serve()

		_, err := b.rev.Undo(t.Context(), undoInvocation(t, byCommit(testCommitSHA)), action.Snapshot{}, false)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "no pull request was made from the branch "+testBranch)
	})

	t.Run("the call does not say what it undoes", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.serve()

		inv := undoInvocation(t, byCommit(testCommitSHA))
		inv.UndoOf = ""

		_, err := b.rev.Undo(t.Context(), inv, action.Snapshot{}, false)
		require.ErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("a snapshot that is not valid", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		h, _ := afterMergedRevert(t, b)

		_, err := b.rev.Undo(t.Context(), undoInvocation(t, byCommit(h.bad)), action.Snapshot{Before: json.RawMessage(`[]`), After: json.RawMessage(`{}`)}, false)
		require.ErrorIs(t, err, action.ErrPermanent)
	})
}

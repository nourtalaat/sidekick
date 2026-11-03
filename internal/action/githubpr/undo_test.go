package githubpr

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// testUndoBranch is the branch of the revert pull request.
var testUndoBranch = "sidekick/" + testUndoID

// Blob SHAs of the files in the undo tests.
const (
	blobRulesBefore = "1111111111111111111111111111111111111111"
	blobOldBefore   = "2222222222222222222222222222222222222222"
)

// undoInvocation returns the invocation that undoes the proposal of
// rulesParams.
func undoInvocation(params Params) action.Invocation {
	inv := invocation(params)
	inv.ProposalID = testUndoID
	inv.IdempotencyKey = action.IdempotencyKey(testUndoID)
	inv.UndoOf = testProposalID

	return inv
}

// undoSnapshot returns the snapshot of a change to the rules file and a new
// file, as Snapshot makes it.
func undoSnapshot() action.Snapshot {
	return action.Snapshot{
		Target:  "github:acme/monitors",
		Version: testBaseSHA,
		Before: json.RawMessage(`{
			"` + testRulesPath + `": {"blob": "` + blobRulesBefore + `"},
			"prometheus/rules/new.yaml": null,
			"prometheus/rules/old.yaml": {"blob": "` + blobOldBefore + `"}}`),
		After: json.RawMessage(`{
			"` + testRulesPath + `": {"blob": "` + blobSHA(testRulesAfter) + `", "status": "changed", "linesAdded": 1, "linesRemoved": 1},
			"prometheus/rules/new.yaml": {"blob": "` + blobSHA("x: 1\n") + `", "status": "added", "linesAdded": 1, "linesRemoved": 0},
			"prometheus/rules/old.yaml": {"status": "deleted", "linesRemoved": 1}}`),
	}
}

// undoParams returns the parameters of the proposal that is undone.
func undoParams() Params {
	p := validParams()
	p.Files = []gitchange.FileChange{
		{Path: testRulesPath, Content: text(testRulesAfter)},
		{Path: "prometheus/rules/new.yaml", Content: text("x: 1\n")},
		{Path: "prometheus/rules/old.yaml", Delete: true},
	}

	return p
}

// errorsAsConflict returns the *action.ConflictError in err.
func errorsAsConflict(err error) (*action.ConflictError, bool) {
	return errors.AsType[*action.ConflictError](err)
}

// itoa writes a number.
func itoa(n int) string {
	return strconv.Itoa(n)
}

func mergedPull(number int) string {
	return `[{"number":` + itoa(number) + `,"draft":false,"state":"closed","html_url":"https://github.com/acme/monitors/pull/` + itoa(number) + `","merged_at":"2026-11-03T10:00:00Z"}]`
}

func openPull(number int) string {
	return `[` + pullBody(number, true) + `]`
}

func TestUndoClosesAnOpenPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{"acme:" + testBranch: openPull(12)})
	b.github.Handle(http.MethodPatch, repoPath("pulls/12"), reply(`{"number":12,"state":"closed","html_url":"https://github.com/acme/monitors/pull/12","closed_at":"2026-11-03T11:00:00Z"}`))
	b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})
	b.github.ExpectAllCalled()

	res, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Equal(t, "Closed the pull request acme/monitors#12 and deleted its branch.", res.Summary)
	require.Equal(t, []action.Link{{Title: "acme/monitors#12", URL: "https://github.com/acme/monitors/pull/12"}}, res.Links)

	require.Equal(t, map[string]any{"state": "closed"}, bodyOf(t, b.github, http.MethodPatch, repoPath("pulls/12")))
	require.Empty(t, b.validator.Requests())
}

func TestUndoOfAClosedPullRequestOnlyDeletesTheBranch(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{"acme:" + testBranch: `[{"number":12,"state":"closed","html_url":"https://github.com/acme/monitors/pull/12","closed_at":"2026-11-03T11:00:00Z"}]`})
	b.github.Handle(http.MethodDelete, repoPath("git/refs/heads/"+testBranch), replyStatus(http.StatusUnprocessableEntity, `{"message":"Reference does not exist"}`))

	res, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err, "a branch that is gone already is not an error")
	require.Equal(t, "The pull request acme/monitors#12 was closed already; its branch is deleted.", res.Summary)

	for _, call := range sent(b.github) {
		require.NotContains(t, call, "PATCH", "a closed pull request is not closed again")
	}
}

func TestUndoRevertsAMergedPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{"acme:" + testBranch: mergedPull(12)})
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testUndoBranch), notFound)
	b.expectBase()
	b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesAfter, blobSHA(testRulesAfter))))
	b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/new.yaml"), reply(fileBodyWithSHA("prometheus/rules/new.yaml", "x: 1\n", blobSHA("x: 1\n"))))
	b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/old.yaml"), notFound)
	b.expectPush()
	b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(30, true)))
	b.github.ExpectAllCalled()

	res, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Opened the revert pull request acme/monitors#30. It still needs review and merging.")
	require.Contains(t, res.Summary, "not validated, because it restores their earlier content")
	require.Equal(t, []action.Link{{Title: "acme/monitors#30", URL: "https://github.com/acme/monitors/pull/30"}}, res.Links)

	tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
	require.Equal(t, testBaseTree, tree["base_tree"])
	require.Equal(t, []any{
		map[string]any{"path": testRulesPath, "mode": "100644", "type": "blob", "sha": blobRulesBefore},
		map[string]any{"path": "prometheus/rules/new.yaml", "sha": nil},
		map[string]any{"path": "prometheus/rules/old.yaml", "mode": "100644", "type": "blob", "sha": blobOldBefore},
	}, tree["tree"], "the earlier blobs are used by SHA, an added file is deleted, a deleted file is brought back")

	commit := bodyOf(t, b.github, http.MethodPost, repoPath("git/commits"))
	require.Equal(t, "Revert: Raise the checkout latency threshold\n\nSidekick proposal "+testUndoID+", which undoes "+testProposalID+"\n", commit["message"])

	ref := bodyOf(t, b.github, http.MethodPost, repoPath("git/refs"))
	require.Equal(t, "refs/heads/"+testUndoBranch, ref["ref"])

	pull := bodyOf(t, b.github, http.MethodPost, repoPath("pulls"))
	require.Equal(t, "Revert: Raise the checkout latency threshold", pull["title"])
	require.Equal(t, testUndoBranch, pull["head"])
	require.Equal(t, "main", pull["base"])
	require.Equal(t, true, pull["draft"])

	body, ok := pull["body"].(string)
	require.True(t, ok)
	require.Contains(t, body, "Reverts the pull request acme/monitors#12")
	require.Contains(t, body, "The files were not validated: this change restores their earlier content.")
	require.Empty(t, b.validator.Requests(), "a revert is not validated")
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	newBedWithEditedFile := func(t *testing.T) *bed {
		t.Helper()

		b := newBed(t, nil)
		b.handlePulls(map[string]string{"acme:" + testBranch: mergedPull(12)})
		b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testUndoBranch), notFound)
		b.expectBase()
		b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, "edited\n", "edited-sha")))
		b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/new.yaml"), reply(fileBodyWithSHA("prometheus/rules/new.yaml", "x: 1\n", blobSHA("x: 1\n"))))
		b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/old.yaml"), notFound)

		return b
	}

	t.Run("without force nothing is written and the files as they are now come back", func(t *testing.T) {
		t.Parallel()

		b := newBedWithEditedFile(t)

		_, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.ErrorIs(t, err, action.ErrConflict)

		conflict, ok := errorsAsConflict(err)
		require.True(t, ok)
		require.JSONEq(t, `{
			"`+testRulesPath+`": {"blob": "edited-sha"},
			"prometheus/rules/new.yaml": {"blob": "`+blobSHA("x: 1\n")+`"},
			"prometheus/rules/old.yaml": null
		}`, string(conflict.Live))

		for _, call := range sent(b.github) {
			require.NotContains(t, call, "POST", "nothing is written")
		}
	})

	t.Run("with force the earlier content is written anyway", func(t *testing.T) {
		t.Parallel()

		b := newBedWithEditedFile(t)
		b.expectPush()
		b.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(31, true)))

		res, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), true)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "revert pull request acme/monitors#31")

		tree := bodyOf(t, b.github, http.MethodPost, repoPath("git/trees"))
		entries, ok := tree["tree"].([]any)
		require.True(t, ok)
		require.Len(t, entries, 3)
	})
}

func TestUndoWithNothingToRevert(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{"acme:" + testBranch: mergedPull(12)})
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testUndoBranch), notFound)
	b.expectBase()
	b.github.Handle(http.MethodGet, repoPath("contents/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesBefore, blobRulesBefore)))
	b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/new.yaml"), notFound)
	b.github.Handle(http.MethodGet, repoPath("contents/prometheus/rules/old.yaml"), reply(fileBodyWithSHA("prometheus/rules/old.yaml", "a\n", blobOldBefore)))

	res, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Equal(t, "Nothing to revert: the files are as they were before the pull request.", res.Summary)

	for _, call := range sent(b.github) {
		require.NotContains(t, call, "POST")
	}
}

func TestUndoAfterACrashFindsTheRevertPullRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handlePulls(map[string]string{"acme:" + testBranch: mergedPull(12), "acme:" + testUndoBranch: openPull(30)})
	b.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+testUndoBranch), reply(refBody("refs/heads/"+testUndoBranch, testCommitSHA)))

	res, err := b.pr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "revert pull request acme/monitors#30")

	for _, call := range sent(b.github) {
		require.NotContains(t, call, "POST", "the revert is not made twice")
	}
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(inv *action.Invocation)
		pulls   map[string]string
		wantErr string
	}{
		{name: "no proposal to undo", change: func(inv *action.Invocation) { inv.UndoOf = "" }, wantErr: "does not say which proposal it undoes"},
		{name: "a proposal ID that is not one", change: func(inv *action.Invocation) { inv.UndoOf = "../main" }, wantErr: "no idempotency key"},
		{name: "no pull request was made", pulls: map[string]string{}, wantErr: "no pull request was made from the branch " + "sidekick/" + testProposalID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.handlePulls(tt.pulls)

			inv := undoInvocation(undoParams())
			if tt.change != nil {
				tt.change(&inv)
			}

			_, err := b.pr.Undo(t.Context(), inv, undoSnapshot(), false)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

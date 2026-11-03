package gitlabmr

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// testUndoBranch is the branch of the revert merge request.
var testUndoBranch = "sidekick/" + testUndoID

// Blob IDs of the files in the undo tests.
const (
	blobRulesBefore = "1111111111111111111111111111111111111111"
	blobOldBefore   = "2222222222222222222222222222222222222222"
	blobEdited      = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	testOldText     = "a\n"
)

// undoInvocation returns the invocation that undoes the proposal of
// undoParams.
func undoInvocation(params Params) action.Invocation {
	inv := invocation(params)
	inv.ProposalID = testUndoID
	inv.IdempotencyKey = action.IdempotencyKey(testUndoID)
	inv.UndoOf = testProposalID

	return inv
}

// undoSnapshot returns the snapshot of a change to the rules file, a new file
// and a deleted file, as Snapshot makes it.
func undoSnapshot() action.Snapshot {
	return action.Snapshot{
		Target:  "gitlab:" + testProject,
		Version: testBaseSHA,
		Before: json.RawMessage(`{
			"` + testRulesPath + `": {"blob": "` + blobRulesBefore + `"},
			"prometheus/rules/new.yaml": null,
			"prometheus/rules/old.yaml": {"blob": "` + blobOldBefore + `"}}`),
		After: json.RawMessage(`{
			"` + testRulesPath + `": {"blob": "` + gitchange.BlobSHA(testRulesAfter) + `", "status": "changed", "linesAdded": 1, "linesRemoved": 1},
			"prometheus/rules/new.yaml": {"blob": "` + gitchange.BlobSHA("x: 1\n") + `", "status": "added", "linesAdded": 1, "linesRemoved": 0},
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

// mergedList is the list of merge requests with one that was merged.
func mergedList(iid int) string {
	return "[" + mergeRequestBody(iid, "merged", false) + "]"
}

// openList is the list of merge requests with one that is open.
func openList(iid int) string {
	return "[" + mergeRequestBody(iid, "opened", true) + "]"
}

// expectBeforeBlobs makes the fake GitLab serve the earlier content of the
// files that a revert brings back.
func (b *bed) expectBeforeBlobs() {
	b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+blobRulesBefore), reply(blobBody(blobRulesBefore, testRulesBefore)))
	b.gitlab.Handle(http.MethodGet, apiPath("repository/blobs/"+blobOldBefore), reply(blobBody(blobOldBefore, testOldText)))
}

// expectMergedFiles makes the fake GitLab serve the files of the change as the
// merged merge request left them.
func (b *bed) expectMergedFiles() {
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+testRulesPath), reply(fileBody(testRulesPath, testRulesAfter)))
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/prometheus/rules/new.yaml"), reply(fileBody("prometheus/rules/new.yaml", "x: 1\n")))
	b.expectNoFile("prometheus/rules/old.yaml")
}

func TestUndoClosesAnOpenMergeRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: openList(12)})
	b.gitlab.Handle(http.MethodPut, apiPath("merge_requests/12"), reply(mergeRequestBody(12, "closed", true)))
	b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})
	b.gitlab.ExpectAllCalled()

	res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Equal(t, "Closed the merge request acme/platform/monitors!12 and deleted its branch.", res.Summary)
	require.Equal(t, []action.Link{{Title: "acme/platform/monitors!12", URL: testWebURL + "12"}}, res.Links)

	require.Equal(t, map[string]any{"state_event": "close"}, bodyOf(t, b.gitlab, http.MethodPut, apiPath("merge_requests/12")), "only the state changes")
	require.Empty(t, b.validator.Requests())
}

func TestUndoOfAClosedMergeRequestOnlyDeletesTheBranch(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: "[" + mergeRequestBody(12, "closed", false) + "]"})
	b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), notFound)

	res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err, "a branch that is gone already is not an error")
	require.Equal(t, "The merge request acme/platform/monitors!12 was closed already; its branch is deleted.", res.Summary)

	for _, call := range sent(b.gitlab) {
		require.NotContains(t, call, "PUT", "a closed merge request is not closed again")
	}
}

func TestUndoRevertsAMergedMergeRequest(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: mergedList(12)})
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
	b.expectBase()
	b.expectMergedFiles()
	b.expectBeforeBlobs()
	b.expectCommit()
	b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(30, "opened", true, testUndoBranch)))
	b.gitlab.ExpectAllCalled()

	res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Contains(t, res.Summary, "Opened the revert merge request acme/platform/monitors!30. It still needs review and merging.")
	require.Contains(t, res.Summary, "not validated, because it restores their earlier content")
	require.Equal(t, []action.Link{{Title: "acme/platform/monitors!30", URL: testWebURL + "30"}}, res.Links)

	commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))
	require.Equal(t, testUndoBranch, commit["branch"])
	require.Equal(t, testBaseSHA, commit["start_sha"])
	require.Equal(t, "Revert: Raise the checkout latency threshold\n\nSidekick proposal "+testUndoID+", which undoes "+testProposalID+"\n", commit["commit_message"])

	actions, err := json.Marshal(commit["actions"])
	require.NoError(t, err)
	require.JSONEq(t, `[
		{"action":"update","file_path":"`+testRulesPath+`","content":`+jsonString(t, testRulesBefore)+`},
		{"action":"delete","file_path":"prometheus/rules/new.yaml"},
		{"action":"create","file_path":"prometheus/rules/old.yaml","content":`+jsonString(t, testOldText)+`}
	]`, string(actions), "the earlier content is brought back, an added file is deleted, a deleted file is made again")

	mr := bodyOf(t, b.gitlab, http.MethodPost, apiPath("merge_requests"))
	require.Equal(t, []string{"description", "source_branch", "target_branch", "title"}, keysOf(mr))
	require.Equal(t, "Draft: Revert: Raise the checkout latency threshold", mr["title"])
	require.Equal(t, testUndoBranch, mr["source_branch"])
	require.Equal(t, "main", mr["target_branch"])

	body, ok := mr["description"].(string)
	require.True(t, ok)
	require.Contains(t, body, "Reverts the merge request `acme/platform/monitors!12`")
	require.Contains(t, body, "The files were not validated: this change restores their earlier content.")
	require.Contains(t, body, "Opened by Sidekick for proposal `"+testUndoID+"`, which undoes `"+testProposalID+"`.")
	require.Empty(t, b.validator.Requests(), "a revert is not validated")

	for _, call := range sent(b.gitlab) {
		require.NotContains(t, call, "revert", "the commit revert call of GitLab is not used")
	}
}

func TestUndoConflict(t *testing.T) {
	t.Parallel()

	newBedWithEditedFile := func(t *testing.T) *bed {
		t.Helper()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{testBranch: mergedList(12)})
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
		b.expectBase()
		b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, "edited\n", blobEdited)))
		b.gitlab.Handle(http.MethodGet, apiPath("repository/files/prometheus/rules/new.yaml"), reply(fileBody("prometheus/rules/new.yaml", "x: 1\n")))
		b.expectNoFile("prometheus/rules/old.yaml")

		return b
	}

	t.Run("without force nothing is written and the files as they are now come back", func(t *testing.T) {
		t.Parallel()

		b := newBedWithEditedFile(t)

		_, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.ErrorIs(t, err, action.ErrConflict)

		conflict, ok := errors.AsType[*action.ConflictError](err)
		require.True(t, ok)
		require.JSONEq(t, `{
			"`+testRulesPath+`": {"blob": "`+blobEdited+`"},
			"prometheus/rules/new.yaml": {"blob": "`+gitchange.BlobSHA("x: 1\n")+`"},
			"prometheus/rules/old.yaml": null
		}`, string(conflict.Live))

		for _, call := range sent(b.gitlab) {
			require.True(t, strings.HasPrefix(call, "GET "), "nothing is written: %s", call)
			require.NotContains(t, call, "blobs", "the earlier content is not read for a revert that stops")
		}
	})

	t.Run("with force the earlier content is written anyway", func(t *testing.T) {
		t.Parallel()

		b := newBedWithEditedFile(t)
		b.expectBeforeBlobs()
		b.expectCommit()
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(31, "opened", true, testUndoBranch)))

		res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), true)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "revert merge request acme/platform/monitors!31")

		commit := bodyOf(t, b.gitlab, http.MethodPost, apiPath("repository/commits"))
		actions, ok := commit["actions"].([]any)
		require.True(t, ok)
		require.Len(t, actions, 3)
	})
}

func TestUndoWithNothingToRevert(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.handleMergeRequests(map[string]string{testBranch: mergedList(12)})
	b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), notFound)
	b.expectBase()
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/"+testRulesPath), reply(fileBodyWithSHA(testRulesPath, testRulesBefore, blobRulesBefore)))
	b.expectNoFile("prometheus/rules/new.yaml")
	b.gitlab.Handle(http.MethodGet, apiPath("repository/files/prometheus/rules/old.yaml"), reply(fileBodyWithSHA("prometheus/rules/old.yaml", testOldText, blobOldBefore)))

	res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
	require.NoError(t, err)
	require.Equal(t, "Nothing to revert: the files are as they were before the merge request.", res.Summary)

	for _, call := range sent(b.gitlab) {
		require.True(t, strings.HasPrefix(call, "GET "), "nothing is written: %s", call)
	}
}

func TestUndoAfterACrash(t *testing.T) {
	t.Parallel()

	t.Run("the revert merge request is found", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{testBranch: mergedList(12), testUndoBranch: "[" + mergeRequestBodyFrom(30, "opened", true, testUndoBranch) + "]"})
		b.gitlab.ExpectAllCalled()

		res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "revert merge request acme/platform/monitors!30")
		require.Equal(t, []string{"GET merge_requests", "GET merge_requests"}, sent(b.gitlab), "the revert is not made twice")
	})

	t.Run("the revert branch has no merge request", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{testBranch: mergedList(12)})
		b.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+testUndoBranch), reply(branchBody(testCommitSHA)))
		b.gitlab.Handle(http.MethodPost, apiPath("merge_requests"), replyStatus(http.StatusCreated, mergeRequestBodyFrom(32, "opened", true, testUndoBranch)))
		b.gitlab.ExpectAllCalled()

		res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "revert merge request acme/platform/monitors!32")

		for _, call := range sent(b.gitlab) {
			require.NotEqual(t, "POST repository/commits", call, "the commit is not made twice")
		}
	})

	t.Run("the closing was done already", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{testBranch: "[" + mergeRequestBody(12, "closed", true) + "]"})
		b.gitlab.Handle(http.MethodDelete, apiPath("repository/branches/"+testBranch), fakehttp.Response{Status: http.StatusNoContent})

		res, err := b.mr.Undo(t.Context(), undoInvocation(undoParams()), undoSnapshot(), false)
		require.NoError(t, err)
		require.Contains(t, res.Summary, "was closed already")
	})
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(inv *action.Invocation)
		wantErr string
		wantIs  error
	}{
		{name: "no proposal to undo", change: func(inv *action.Invocation) { inv.UndoOf = "" }, wantErr: "does not say which proposal it undoes", wantIs: action.ErrPermanent},
		{name: "a proposal ID that is not one", change: func(inv *action.Invocation) { inv.UndoOf = "../main" }, wantErr: "no idempotency key", wantIs: action.ErrPermanent},
		{name: "no merge request was made", wantErr: "no merge request was made from the branch sidekick/" + testProposalID, wantIs: action.ErrPermanent},
		{
			name: "a project outside the limits", wantIs: action.ErrLimit, wantErr: "is not one of limits.projects",
			change: func(inv *action.Invocation) {
				p := undoParams()
				p.Project = "acme/platform/other"

				raw, err := json.Marshal(p)
				if err != nil {
					panic(err)
				}

				inv.Params = raw
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.handleMergeRequests(nil)

			inv := undoInvocation(undoParams())
			if tt.change != nil {
				tt.change(&inv)
			}

			_, err := b.mr.Undo(t.Context(), inv, undoSnapshot(), false)
			require.ErrorIs(t, err, tt.wantIs)
			require.ErrorContains(t, err, tt.wantErr)

			for _, call := range sent(b.gitlab) {
				require.True(t, strings.HasPrefix(call, "GET "), "nothing is written: %s", call)
			}
		})
	}
}

// mergeRequestBodyFrom is mergeRequestBody for a merge request from another
// source branch.
func mergeRequestBodyFrom(iid int, state string, draft bool, branch string) string {
	return strings.Replace(mergeRequestBody(iid, state, draft), `"source_branch":"`+testBranch+`"`, `"source_branch":"`+branch+`"`, 1)
}

//go:build integration

package gitlabcode

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// proposeInput returns the propose_action call with the parameters.
func proposeInput(t *testing.T, p Params) json.RawMessage {
	t.Helper()

	input, err := json.Marshal(map[string]any{
		"action": "code-mr",
		"params": invocation(p).Params,
		"reason": "The total of an invoice is wrong.",
	})
	require.NoError(t, err)

	return input
}

func TestProposeAndRunThroughTheService(t *testing.T) {
	t.Parallel()

	s := newStack(t)

	propose := func(callID string, p Params) proposals.ProposeResult {
		res, err := s.svc.Propose(t.Context(), proposals.ProposeRequest{
			InvestigationID: checkoutInvestigation, ToolCallID: callID, Route: routeName, Mode: action.Approved, Input: proposeInput(t, p),
		})
		require.NoError(t, err)

		return res
	}

	// A path that is a symlink is refused with its reason, and nothing is
	// stored: the model does not hear "try again later".
	s.serveRepo(repo{testPath: node{text: "target", mode: "120000"}})

	refused := propose("call-1", codeParams())
	require.Contains(t, refused.Refusal, "is a symlink")
	require.NotContains(t, refused.Refusal, "try again later")
	require.Equal(t, 0, s.proposalCount(t))

	// A refused path is refused too, with no call to GitLab.
	ci := validParams()
	ci.Files = change(".github/workflows/ci.yml", "on: push\n")

	refusedPath := propose("call-2", ci)
	require.Contains(t, refusedPath.Refusal, "CI and build automation")
	require.Equal(t, 0, s.proposalCount(t))
}

func TestProposeApproveAndRun(t *testing.T) {
	t.Parallel()

	s := newStack(t)
	s.serveRepo(repo{testPath: regular(testBefore)})

	stored, err := s.svc.Propose(t.Context(), proposals.ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: "call-1", Route: routeName, Mode: action.Approved, Input: proposeInput(t, codeParams()),
	})
	require.NoError(t, err)
	require.Empty(t, stored.Refusal)
	require.Equal(t, 1, s.proposalCount(t))

	view, err := proposals.Get(t.Context(), s.pool, stored.ID)
	require.NoError(t, err)
	require.NotNil(t, view.Snapshot)
	require.Equal(t, "gitlab:"+testProject, view.Snapshot.Target)
	require.Equal(t, testBaseSHA, view.Snapshot.Version)
	require.NotContains(t, string(view.Snapshot.After), "return", "a snapshot holds no file contents")
	require.Len(t, view.Files, 1, "the proposal lists its files as it does for a pull request")
	require.Equal(t, testPath, view.Files[0].Path)

	_, err = s.svc.Approve(t.Context(), stored.ID, proposals.Decider{UserID: anaUser})
	require.NoError(t, err)

	branch := "sidekick/" + stored.ID.String()
	s.handleMergeRequests(nil)
	s.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+branch), notFound)
	s.expectCommit()
	s.expectOpen(21, true)

	require.NoError(t, s.runJob(t))

	row, err := db.New(s.pool).GetActionProposal(t.Context(), stored.ID)
	require.NoError(t, err)
	require.Equal(t, string(action.StatusSucceeded), row.Status)
	require.True(t, row.Undoable, "the action can undo what it opens")

	var result action.Result

	require.NoError(t, json.Unmarshal(row.Result, &result))
	require.Equal(t, "Opened draft merge request "+testProject+"!21", result.Summary)
	require.Equal(t, &action.PullRequestRef{Number: 21, HeadSHA: testCommitSHA}, result.PullRequest, "the result carries the head SHA and the IID for the CI report")

	mr := bodyOf(t, s.gitlab, http.MethodPost, apiPath("merge_requests"))
	require.Equal(t, branch, mr["source_branch"])
	require.Equal(t, "Draft: Fix the total of an invoice", mr["title"])
}

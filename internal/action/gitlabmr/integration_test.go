//go:build integration

package gitlabmr

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// proposeInput returns the propose_action call that changes the rules file.
func proposeInput(t *testing.T) json.RawMessage {
	t.Helper()

	input, err := json.Marshal(map[string]any{
		"action": "monitor-mr",
		"params": invocation(rulesParams()).Params,
		"reason": "The alert fires on one-minute spikes.",
	})
	require.NoError(t, err)

	return input
}

func TestProposeAndRunThroughTheService(t *testing.T) {
	t.Parallel()

	s := newStack(t)

	propose := func(callID string) proposals.ProposeResult {
		res, err := s.svc.Propose(t.Context(), proposals.ProposeRequest{
			InvestigationID: checkoutInvestigation, ToolCallID: callID, Route: routeName, Mode: action.Approved, Input: proposeInput(t),
		})
		require.NoError(t, err)

		return res
	}

	// The validator finds an error: the model gets it and nothing is stored.
	s.expectBase()
	s.expectFile(testRulesPath, testRulesBefore)
	s.validator.Handle(http.MethodPost, "/validate", reply(`{"ok":false,"errors":[{"path":"`+testRulesPath+`","line":6,"message":"bad duration"}]}`))

	refused := propose("call-1")
	require.Contains(t, refused.Refusal, testRulesPath+":6: bad duration")
	require.Contains(t, refused.Refusal, "no pull request is opened")
	require.Equal(t, 0, s.proposalCount(t))

	for _, call := range sent(s.gitlab) {
		require.True(t, strings.HasPrefix(call, "GET "), "a refused proposal makes no branch: %s", call)
	}

	// The model fixes the file: the check passes, the snapshot reads the
	// files again, the proposal is stored, and approving it queues the job
	// that opens the merge request.
	s.expectBase()
	s.expectBase()
	s.expectFile(testRulesPath, testRulesBefore)
	s.expectFile(testRulesPath, testRulesBefore)
	s.validatorPasses(2)

	stored := propose("call-2")
	require.Empty(t, stored.Refusal)
	require.Equal(t, 1, s.proposalCount(t))

	view, err := proposals.Get(t.Context(), s.pool, stored.ID)
	require.NoError(t, err)
	require.NotNil(t, view.Snapshot)
	require.Equal(t, "gitlab:"+testProject, view.Snapshot.Target)
	require.Equal(t, testBaseSHA, view.Snapshot.Version)
	require.NotContains(t, string(view.Snapshot.After), "groups:", "a snapshot holds no file contents")
	require.Len(t, view.Files, 1, "the proposal lists its files as it does for a pull request")
	require.Equal(t, testRulesPath, view.Files[0].Path)

	paths := make([]string, 0, len(view.Changes))
	for _, c := range view.Changes {
		paths = append(paths, c.Path)
	}

	require.Contains(t, paths, testRulesPath+".status", "the approver reads what happens to each file")
	require.Contains(t, paths, testRulesPath+".linesAdded")

	_, err = s.svc.Approve(t.Context(), stored.ID, proposals.Decider{UserID: anaUser})
	require.NoError(t, err)

	branch := "sidekick/" + stored.ID.String()
	s.handleMergeRequests(nil)
	s.gitlab.Handle(http.MethodGet, apiPath("repository/branches/"+branch), notFound)
	s.expectBase()
	s.expectFile(testRulesPath, testRulesBefore)
	s.expectCommit()
	s.expectOpen(21, true)

	require.NoError(t, s.runJob(t))

	row, err := db.New(s.pool).GetActionProposal(t.Context(), stored.ID)
	require.NoError(t, err)
	require.Equal(t, string(action.StatusSucceeded), row.Status)
	require.True(t, row.Undoable, "the action can undo what it opens")

	var result action.Result

	require.NoError(t, json.Unmarshal(row.Result, &result))
	require.Equal(t, "Opened draft merge request acme/platform/monitors!21", result.Summary)
	require.Equal(t, []action.Link{{Title: "acme/platform/monitors!21", URL: testWebURL + "21"}}, result.Links)
	require.Equal(t, &action.PullRequestRef{Number: 21, HeadSHA: testCommitSHA}, result.PullRequest)

	mr := bodyOf(t, s.gitlab, http.MethodPost, apiPath("merge_requests"))
	require.Equal(t, branch, mr["source_branch"])
}

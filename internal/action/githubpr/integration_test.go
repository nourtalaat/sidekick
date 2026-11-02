//go:build integration

package githubpr

import (
	"encoding/json"
	"net/http"
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
		"action": "monitor-pr",
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

	// The model fixes the file: the proposal is stored, and approving it
	// queues the job that opens the pull request.
	s.expectBase()
	s.expectFile(testRulesPath, testRulesBefore)
	s.validatorPasses(2)

	stored := propose("call-2")
	require.Empty(t, stored.Refusal)
	require.Equal(t, 1, s.proposalCount(t))

	_, err := s.svc.Approve(t.Context(), stored.ID, proposals.Decider{UserID: anaUser})
	require.NoError(t, err)

	branch := "sidekick/" + stored.ID.String()
	s.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+branch), notFound)
	s.expectBase()
	s.expectFile(testRulesPath, testRulesBefore)
	s.expectPush()
	s.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(21, true)))

	require.NoError(t, s.runJob(t))

	row, err := db.New(s.pool).GetActionProposal(t.Context(), stored.ID)
	require.NoError(t, err)
	require.Equal(t, string(action.StatusSucceeded), row.Status)

	var result action.Result

	require.NoError(t, json.Unmarshal(row.Result, &result))
	require.Equal(t, "Opened draft pull request acme/monitors#21", result.Summary)
	require.Equal(t, []action.Link{{Title: "acme/monitors#21", URL: "https://github.com/acme/monitors/pull/21"}}, result.Links)

	pull := bodyOf(t, s.github, http.MethodPost, repoPath("pulls"))
	require.Equal(t, branch, pull["head"])
}

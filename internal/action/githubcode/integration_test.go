//go:build integration

package githubcode

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/proposals"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// proposeInput returns the propose_action call that changes the Go file.
func proposeInput(t *testing.T) json.RawMessage {
	t.Helper()

	input, err := json.Marshal(map[string]any{
		"action": "code-change",
		"params": invocation(validParams()).Params,
		"reason": "The total ignores the second line.",
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

	// The file is a symlink at the base: the model gets the reason and
	// nothing is stored.
	s.serveBase(repoFiles{testGoPath: {mode: modeSymlink, typ: typeBlob, content: "other.go"}})

	refused := propose("call-1")
	require.Contains(t, refused.Refusal, "is a symlink")
	require.Equal(t, 0, s.proposalCount(t))

	// The file is a regular file: the proposal is stored with its snapshot,
	// and approving it queues the job that opens the pull request.
	s.serveBase(repoFiles{testGoPath: regular(testGoBefore)})

	stored := propose("call-2")
	require.Empty(t, stored.Refusal)
	require.Equal(t, 1, s.proposalCount(t))

	view, err := proposals.Get(t.Context(), s.pool, stored.ID)
	require.NoError(t, err)
	require.NotNil(t, view.Snapshot)
	require.Equal(t, "github:acme/app", view.Snapshot.Target)
	require.Equal(t, testBaseSHA, view.Snapshot.Version)
	require.NotContains(t, string(view.Snapshot.After), "package billing", "a snapshot holds no file contents")
	require.Len(t, view.Files, 1)
	require.Equal(t, proposalFile(testGoPath, "changed", 1, 1), view.Files[0])

	_, err = s.svc.Approve(t.Context(), stored.ID, proposals.Decider{UserID: anaUser})
	require.NoError(t, err)

	branch := "sidekick/" + stored.ID.String()
	s.github.Handle(http.MethodGet, repoPath("git/ref/heads/"+branch), notFound)
	s.github.Handle(http.MethodPost, repoPath("git/trees"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"sha":%q}`, testTreeSHA)))
	s.github.Handle(http.MethodPost, repoPath("git/commits"), replyStatus(http.StatusCreated, fmt.Sprintf(`{"sha":%q}`, testCommitSHA)))
	s.github.Handle(http.MethodPost, repoPath("git/refs"), replyStatus(http.StatusCreated, refBody("refs/heads/"+branch, testCommitSHA)))
	s.github.Handle(http.MethodPost, repoPath("pulls"), replyStatus(http.StatusCreated, pullBody(21, true)))

	require.NoError(t, s.runJob(t))

	row, err := db.New(s.pool).GetActionProposal(t.Context(), stored.ID)
	require.NoError(t, err)
	require.Equal(t, string(action.StatusSucceeded), row.Status)
	require.True(t, row.Undoable, "the action can undo what it opens")
	require.Nil(t, row.FixKind, "a code change is not a monitor fix")

	var result action.Result

	require.NoError(t, json.Unmarshal(row.Result, &result))
	require.Equal(t, "Opened draft pull request acme/app#21", result.Summary)
	require.Equal(t, []action.Link{{Title: "acme/app#21", URL: "https://github.com/acme/app/pull/21"}}, result.Links)
	require.Equal(t, &action.PullRequestRef{Number: 21, HeadSHA: testCommitSHA}, result.PullRequest)

	pull := bodyOf(t, s.github, http.MethodPost, repoPath("pulls"))
	require.Equal(t, branch, pull["head"])
	require.Equal(t, true, pull["draft"])
}

// proposalFile is a file of a proposal as the approver reads it.
func proposalFile(path, status string, added, removed int) action.ProposalFile {
	return action.ProposalFile{Path: path, Status: status, LinesAdded: added, LinesRemoved: removed}
}

func TestFileDiffReadsTheBaseFromGitHub(t *testing.T) {
	t.Parallel()

	s := newStack(t)

	// The proposal is made as the model would: the base is read when it
	// proposes, and the diff reads the before side again from GitHub.
	s.serveBase(repoFiles{testGoPath: regular(testGoBefore)})

	res, err := s.svc.Propose(t.Context(), proposals.ProposeRequest{
		InvestigationID: checkoutInvestigation, ToolCallID: "call-1", Route: routeName, Mode: action.Approved, Input: proposeInput(t),
	})
	require.NoError(t, err)
	require.Empty(t, res.Refusal)

	got, err := s.svc.FileDiff(t.Context(), res.ID, testGoPath, false)
	require.NoError(t, err)
	require.Equal(t, gitchange.FileChanged, got.Status)
	require.Contains(t, renderLines(got.Lines), "-4/. \\u0009return 1")
	require.Contains(t, renderLines(got.Lines), "+./4 \\u0009return 2")
	require.False(t, got.Truncated)
}

// renderLines writes lines as the tests read them: the sign, the old and the
// new number, and the text.
func renderLines(lines []gitchange.Line) []string {
	sign := map[string]string{gitchange.LineContext: " ", gitchange.LineAdded: "+", gitchange.LineRemoved: "-"}
	number := func(n int) string {
		if n == 0 {
			return "."
		}

		return fmt.Sprint(n)
	}

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, fmt.Sprintf("%s%s/%s %s", sign[l.Kind], number(l.Old), number(l.New), l.Text))
	}

	return out
}

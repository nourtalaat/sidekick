package githubgit

import (
	"net/http"
	"testing"

	gh "github.com/google/go-github/v93/github"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestCloseOriginal(t *testing.T) {
	t.Parallel()

	const (
		branch    = "sidekick/0192d3a4-0000-7000-8000-0000000000a1"
		undoID    = "0192d3a4-0000-7000-8000-0000000000a2"
		proposal  = "0192d3a4-0000-7000-8000-0000000000a1"
		linkTitle = "acme/api#7"
	)

	inv := action.Invocation{ProposalID: undoID, UndoOf: proposal, IdempotencyKey: action.IdempotencyKey(undoID)}

	t.Run("an open pull request is closed and its branch deleted", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeClient(t)
		fake.Handle(http.MethodPatch, "/repos/acme/api/pulls/7", responseOf(pullJSON(7, "closed", true)))
		fake.Handle(http.MethodDelete, "/repos/acme/api/git/refs/heads/"+branch, noContent())

		m, err := NewMetrics(nil, "githubgitclose")
		require.NoError(t, err)

		res, err := CloseOriginal(t.Context(), client, nil, m, inv, "acme/api", &gh.PullRequest{Number: new(7), State: new("open"), HTMLURL: new("https://github.com/acme/api/pull/7")}, branch)
		require.NoError(t, err)
		require.Equal(t, "Closed the pull request acme/api#7 and deleted its branch.", res.Summary)
		require.Equal(t, []action.Link{{Title: linkTitle, URL: "https://github.com/acme/api/pull/7"}}, res.Links)
		require.Equal(t, []string{"PATCH /repos/acme/api/pulls/7", "DELETE /repos/acme/api/git/refs/heads/" + branch}, calls(fake))
	})

	t.Run("a closed pull request only has its branch deleted", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeClient(t)
		fake.Handle(http.MethodDelete, "/repos/acme/api/git/refs/heads/"+branch, noContent())

		m, err := NewMetrics(nil, "githubgitclose")
		require.NoError(t, err)

		res, err := CloseOriginal(t.Context(), client, nil, m, inv, "acme/api", &gh.PullRequest{Number: new(7), State: new("closed")}, branch)
		require.NoError(t, err)
		require.Equal(t, "The pull request acme/api#7 was closed already; its branch is deleted.", res.Summary)
		require.Equal(t, []string{"DELETE /repos/acme/api/git/refs/heads/" + branch}, calls(fake))
	})

	t.Run("a pull request that cannot be closed", func(t *testing.T) {
		t.Parallel()

		client, fake := newFakeClient(t)
		fake.Handle(http.MethodPatch, "/repos/acme/api/pulls/7", fakehttpResponse(http.StatusForbidden, `{"message":"Forbidden"}`))

		m, err := NewMetrics(nil, "githubgitclose")
		require.NoError(t, err)

		_, err = CloseOriginal(t.Context(), client, nil, m, inv, "acme/api", &gh.PullRequest{Number: new(7), State: new("open")}, branch)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorContains(t, err, "close the original")
	})
}

func TestRecordWithoutADatabaseDoesNothing(t *testing.T) {
	t.Parallel()

	require.NoError(t, Record(t.Context(), nil, "0192d3a4-0000-7000-8000-0000000000a1", "acme/api", &gitchange.Status{Number: 7, State: gitchange.StateClosed}))
}

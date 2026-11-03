package githubrevert

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestPullRequestState(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.ownPull(12, testBranch, "closed", true)
	b.w.serve()

	params := marshal(t, byCommit(testSHA))

	repo, st, err := b.rev.PullRequestState(t.Context(), params, testBranch)
	require.NoError(t, err)
	require.Equal(t, testRepo, repo)
	require.Equal(t, 12, st.Number)
	require.Equal(t, gitchange.StateMerged, st.State)
	require.NotNil(t, st.MergedAt)

	_, st, err = b.rev.PullRequestState(t.Context(), params, "sidekick/other")
	require.NoError(t, err)
	require.Nil(t, st, "no pull request from that branch")

	_, _, err = b.rev.PullRequestState(t.Context(), []byte(`{"repo":"not a repo"}`), testBranch)
	require.ErrorIs(t, err, action.ErrPermanent)
	require.NotErrorIs(t, err, action.ErrLimit)
}

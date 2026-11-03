package gitlabrevert

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
)

func TestPullRequestState(t *testing.T) {
	t.Parallel()

	t.Run("a merge request that was merged", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.w.addMergeRequest(&fakeMR{IID: 7, State: "merged", Draft: true, Source: testBranch})

		project, st, err := b.rv.PullRequestState(t.Context(), invocation(commitParams()).Params, testBranch)
		require.NoError(t, err)
		require.Equal(t, testProject, project)
		require.Equal(t, 7, st.Number)
		require.Equal(t, gitchange.StateMerged, st.State)
		require.NotNil(t, st.MergedAt)
	})

	t.Run("no merge request", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		project, st, err := b.rv.PullRequestState(t.Context(), invocation(commitParams()).Params, testBranch)
		require.NoError(t, err)
		require.Equal(t, testProject, project)
		require.Nil(t, st)
	})

	t.Run("a project that is not allowed any more", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		p := commitParams()
		p.Project = "acme/other/project"

		raw, err := json.Marshal(p)
		require.NoError(t, err)

		_, _, err = b.rv.PullRequestState(t.Context(), raw, testBranch)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, b.w.srv.Requests())
	})

	t.Run("parameters with no project", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		_, _, err := b.rv.PullRequestState(t.Context(), json.RawMessage(`{"commit":"x"}`), testBranch)
		require.ErrorIs(t, err, action.ErrPermanent)
	})
}

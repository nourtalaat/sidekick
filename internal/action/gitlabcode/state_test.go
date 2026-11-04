package gitlabcode

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestPullRequestState(t *testing.T) {
	t.Parallel()

	params := []byte(`{"project":"` + testProject + `"}`)

	t.Run("a merged merge request", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(map[string]string{testBranch: listOf(mergeRequestBody(12, "merged", false))})

		project, st, err := b.code.PullRequestState(t.Context(), params, testBranch)
		require.NoError(t, err)
		require.Equal(t, testProject, project)
		require.Equal(t, 12, st.Number)
		require.Equal(t, gitchange.StateMerged, st.State)
		require.NotNil(t, st.MergedAt)
		require.Equal(t, []string{"GET merge_requests"}, sent(b.gitlab))
	})

	t.Run("no merge request", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.handleMergeRequests(nil)

		project, st, err := b.code.PullRequestState(t.Context(), params, testBranch)
		require.NoError(t, err)
		require.Equal(t, testProject, project)
		require.Nil(t, st)
	})

	t.Run("a project the config no longer lists is not read", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		_, _, err := b.code.PullRequestState(t.Context(), []byte(`{"project":"acme/other"}`), testBranch)
		require.ErrorIs(t, err, action.ErrPermanent)
		require.ErrorIs(t, err, action.ErrLimit)
		require.Empty(t, b.gitlab.Requests())
	})

	t.Run("parameters with no project", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)

		for _, bad := range []string{`not json`, `{}`, `{"project":"../x"}`} {
			_, _, err := b.code.PullRequestState(t.Context(), []byte(bad), testBranch)
			require.ErrorIs(t, err, action.ErrPermanent, bad)
		}

		require.Empty(t, b.gitlab.Requests())
	})

	t.Run("a server error is tried again", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), failed(http.StatusInternalServerError, "boom"))

		_, _, err := b.code.PullRequestState(t.Context(), params, testBranch)
		require.Error(t, err)
		require.NotErrorIs(t, err, action.ErrPermanent)
	})
}

func TestCountCheckUsesTheMetricOfThisAction(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	secrets := registry.NewMockSecrets(gomock.NewController(t))

	code, err := newCode("code-mr", Config{TokenSecret: testToken, Limits: validLimits()}, registry.Deps{Secrets: secrets, Metrics: reg})
	require.NoError(t, err)

	code.CountCheck("merged")

	families, err := reg.Gather()
	require.NoError(t, err)

	var names []string
	for _, f := range families {
		names = append(names, f.GetName())
	}

	require.Contains(t, names, "sidekick_gitlabcode_status_checks_total")
}

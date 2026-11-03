package gitlabmr

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestPullRequestState(t *testing.T) {
	t.Parallel()

	merged := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	closed := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		list string
		want *gitchange.Status
	}{
		{name: "no merge request", list: "[]"},
		{
			name: "an open merge request",
			list: `[{"iid":4,"state":"opened","draft":true,"web_url":"` + testWebURL + `4"}]`,
			want: &gitchange.Status{Number: 4, URL: testWebURL + "4", State: gitchange.StateOpen},
		},
		{
			name: "a merge request that is being merged",
			list: `[{"iid":4,"state":"locked","web_url":"` + testWebURL + `4"}]`,
			want: &gitchange.Status{Number: 4, URL: testWebURL + "4", State: gitchange.StateOpen},
		},
		{
			name: "a merged merge request",
			list: `[{"iid":5,"state":"merged","web_url":"` + testWebURL + `5","merged_at":"2026-11-03T10:00:00.000Z","closed_at":"2026-11-03T10:00:00.000Z"}]`,
			want: &gitchange.Status{Number: 5, URL: testWebURL + "5", State: gitchange.StateMerged, MergedAt: &merged},
		},
		{
			name: "a merged merge request that has no time",
			list: `[{"iid":5,"state":"merged","web_url":"` + testWebURL + `5","merged_at":null}]`,
			want: &gitchange.Status{Number: 5, URL: testWebURL + "5", State: gitchange.StateMerged},
		},
		{
			name: "a merge request closed without a merge",
			list: `[{"iid":6,"state":"closed","web_url":"` + testWebURL + `6","closed_at":"2026-11-04T09:30:00Z","merged_at":null}]`,
			want: &gitchange.Status{Number: 6, URL: testWebURL + "6", State: gitchange.StateClosed, ClosedAt: &closed},
		},
		{
			name: "the open one among several",
			list: `[{"iid":8,"state":"closed","web_url":"` + testWebURL + `8"},{"iid":7,"state":"opened","web_url":"` + testWebURL + `7"}]`,
			want: &gitchange.Status{Number: 7, URL: testWebURL + "7", State: gitchange.StateOpen},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.handleMergeRequests(map[string]string{testBranch: tt.list})
			b.gitlab.ExpectAllCalled()

			repo, got, err := b.mr.PullRequestState(t.Context(), json.RawMessage(`{"project":"`+testProject+`"}`), testBranch)
			require.NoError(t, err)
			require.Equal(t, testProject, repo)
			require.Equal(t, tt.want, got)

			list := queryOf(t, b.gitlab, http.MethodGet, apiPath("merge_requests"))
			require.Equal(t, testBranch, list.Get("source_branch"))
			require.Equal(t, "all", list.Get("state"))
		})
	}
}

func TestPullRequestStateErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		params    string
		wantErrIs error
	}{
		{name: "parameters that are not JSON", params: `{`, wantErrIs: action.ErrPermanent},
		{name: "a project that is not a path", params: `{"project":"acme"}`, wantErrIs: action.ErrPermanent},
		{name: "a project the config does not allow", params: `{"project":"acme/platform/other"}`, wantErrIs: action.ErrLimit},
		{name: "a project the config no longer lists, as another case", params: `{"project":"Acme/Platform/Other"}`, wantErrIs: action.ErrPermanent},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			_, _, err := b.mr.PullRequestState(t.Context(), json.RawMessage(tt.params), testBranch)
			require.ErrorIs(t, err, tt.wantErrIs)
			require.ErrorIs(t, err, action.ErrPermanent)
			require.Empty(t, b.gitlab.Requests(), "nothing is read for a project the action may not read")
		})
	}

	t.Run("a project the token cannot read is skipped for good", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), notFound)

		_, _, err := b.mr.PullRequestState(t.Context(), json.RawMessage(`{"project":"`+testProject+`"}`), testBranch)
		require.ErrorIs(t, err, action.ErrPermanent)
	})

	t.Run("a GitLab that fails is tried again", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.gitlab.Handle(http.MethodGet, apiPath("merge_requests"), failed(http.StatusBadGateway, "bad gateway"))

		_, _, err := b.mr.PullRequestState(t.Context(), json.RawMessage(`{"project":"`+testProject+`"}`), testBranch)
		require.Error(t, err)
		require.False(t, errors.Is(err, action.ErrPermanent))
	})
}

func TestCountCheckKeepsTheGitLabMetric(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	mr, err := newMR("monitor-mr", Config{TokenSecret: testToken, Limits: validLimits()}, registry.Deps{
		Secrets: registrySecrets(t),
		Metrics: reg,
	})
	require.NoError(t, err)

	mr.CountCheck(gitchange.StateMerged)
	mr.CountCheck(gitchange.StateMerged)
	mr.CountCheck("error")

	families, err := reg.Gather()
	require.NoError(t, err)

	got := map[string]float64{}

	for _, family := range families {
		if family.GetName() != "sidekick_gitlabmr_status_checks_total" {
			continue
		}

		for _, m := range family.GetMetric() {
			require.Len(t, m.GetLabel(), 1)
			require.Equal(t, "state", m.GetLabel()[0].GetName())

			got[m.GetLabel()[0].GetValue()] = m.GetCounter().GetValue()
		}
	}

	require.Equal(t, map[string]float64{"merged": 2, "error": 1}, got)
}

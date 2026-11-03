package githubcode

import (
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

	tests := []struct {
		name string
		list string
		want *gitchange.Status
	}{
		{name: "no pull request", list: "[]"},
		{
			name: "an open pull request",
			list: `[{"number":4,"state":"open","draft":true,"html_url":"https://github.com/acme/app/pull/4"}]`,
			want: &gitchange.Status{Number: 4, URL: "https://github.com/acme/app/pull/4", State: gitchange.StateOpen},
		},
		{
			name: "a merged pull request",
			list: `[{"number":5,"state":"closed","html_url":"https://github.com/acme/app/pull/5","merged_at":"2026-11-03T10:00:00Z"}]`,
			want: &gitchange.Status{Number: 5, URL: "https://github.com/acme/app/pull/5", State: gitchange.StateMerged, MergedAt: &merged},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.github.Handle(http.MethodGet, repoPath("pulls"), reply(tt.list))

			repo, got, err := b.code.PullRequestState(t.Context(), rawParams(t, validParams()), testBranch)
			require.NoError(t, err)
			require.Equal(t, testRepo, repo)
			require.Equal(t, tt.want, got)

			list := b.github.Requests()[1]
			require.Equal(t, "acme:"+testBranch, list.Query.Get("head"))
			require.Equal(t, "all", list.Query.Get("state"))
		})
	}
}

func TestPullRequestStateErrors(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	_, _, err := b.code.PullRequestState(t.Context(), []byte(`{"repo":"../x"}`), testBranch)
	require.ErrorIs(t, err, action.ErrPermanent)

	b.github.Handle(http.MethodGet, repoPath("pulls"), notFound)

	_, _, err = b.code.PullRequestState(t.Context(), rawParams(t, validParams()), testBranch)
	require.ErrorIs(t, err, action.ErrPermanent)
}

func TestCountCheckUsesTheOwnMetric(t *testing.T) {
	t.Parallel()

	reg := prometheus.NewRegistry()

	code, err := newCode("code-change", Config{App: testApp, Limits: validLimits()}, registry.Deps{Secrets: secretsWithKey(t), Metrics: reg})
	require.NoError(t, err)

	code.CountCheck(gitchange.StateMerged)
	code.CountCheck(gitchange.StateMerged)
	code.CountCheck("error")

	families, err := reg.Gather()
	require.NoError(t, err)

	got := map[string]float64{}

	for _, family := range families {
		if family.GetName() != "sidekick_githubcode_status_checks_total" {
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

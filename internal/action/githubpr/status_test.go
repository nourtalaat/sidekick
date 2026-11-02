package githubpr

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

func TestStatus(t *testing.T) {
	t.Parallel()

	merged := time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)
	closed := time.Date(2026, 11, 4, 9, 30, 0, 0, time.UTC)

	tests := []struct {
		name string
		list string
		want *Status
	}{
		{name: "no pull request", list: "[]"},
		{
			name: "an open pull request",
			list: `[{"number":4,"state":"open","draft":true,"html_url":"https://github.com/acme/monitors/pull/4"}]`,
			want: &Status{Number: 4, URL: "https://github.com/acme/monitors/pull/4", State: StateOpen},
		},
		{
			name: "a merged pull request",
			list: `[{"number":5,"state":"closed","html_url":"https://github.com/acme/monitors/pull/5","merged_at":"2026-11-03T10:00:00Z","closed_at":"2026-11-03T10:00:00Z"}]`,
			want: &Status{Number: 5, URL: "https://github.com/acme/monitors/pull/5", State: StateMerged, MergedAt: &merged},
		},
		{
			name: "a pull request closed without a merge",
			list: `[{"number":6,"state":"closed","html_url":"https://github.com/acme/monitors/pull/6","closed_at":"2026-11-04T09:30:00Z","merged_at":null}]`,
			want: &Status{Number: 6, URL: "https://github.com/acme/monitors/pull/6", State: StateClosed, ClosedAt: &closed},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.github.Handle(http.MethodGet, repoPath("pulls"), reply(tt.list))
			b.github.ExpectAllCalled()

			got, err := b.pr.status(t.Context(), "acme", "monitors", testBranch)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)

			list := b.github.Requests()[1]
			require.Equal(t, "acme:"+testBranch, list.Query.Get("head"))
			require.Equal(t, "all", list.Query.Get("state"))
		})
	}
}

func TestStatusGitHubError(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.github.Handle(http.MethodGet, repoPath("pulls"), notFound)

	_, err := b.pr.status(t.Context(), "acme", "monitors", testBranch)
	require.Error(t, err)
	require.True(t, hasPermanent(err))
}

func TestStatusCheckerEnabled(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	enabled, err := NewStatusChecker(StatusOptions{Actions: map[string]action.Action{"monitor-pr": b.pr}})
	require.NoError(t, err)
	require.True(t, enabled.Enabled())
	require.Equal(t, []string{"monitor-pr"}, enabled.names)

	none, err := NewStatusChecker(StatusOptions{Actions: map[string]action.Action{}})
	require.NoError(t, err)
	require.False(t, none.Enabled())
}

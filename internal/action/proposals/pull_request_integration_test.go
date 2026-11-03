//go:build integration

package proposals

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// pullRequestCases are the proposals of TestPullRequestState: one for each
// state of a pull request, and one with no row in pull_request_states.
func pullRequestCases(checkedAt time.Time) map[string]*action.PullRequest {
	mergedAt := checkedAt.Add(-time.Hour)
	closedAt := checkedAt.Add(-2 * time.Hour)

	return map[string]*action.PullRequest{
		"open": {
			Repo: "acme/monitors", Number: 11, URL: "https://github.com/acme/monitors/pull/11", State: "open", CheckedAt: checkedAt,
		},
		"merged": {
			Repo: "acme/monitors", Number: 12, URL: "https://github.com/acme/monitors/pull/12", State: "merged", MergedAt: &mergedAt, CheckedAt: checkedAt,
		},
		"closed": {
			Repo: "acme/monitors", Number: 13, URL: "https://github.com/acme/monitors/pull/13", State: "closed", ClosedAt: &closedAt, CheckedAt: checkedAt,
		},
		"missing": nil,
	}
}

func TestPullRequestState(t *testing.T) {
	t.Parallel()

	h := setup(t)
	checkedAt := time.Now().UTC().Truncate(time.Second)
	want := pullRequestCases(checkedAt)
	ids := map[string]uuid.UUID{}

	for name, pr := range want {
		id := h.propose(t, "call-"+name, noteInput)
		ids[name] = id

		if pr == nil {
			continue
		}

		err := db.New(h.pool).RecordPullRequestState(t.Context(), db.RecordPullRequestStateParams{
			ProposalID: id, Repo: pr.Repo, Number: pr.Number, Url: pr.URL, State: pr.State,
			MergedAt: pr.MergedAt, ClosedAt: pr.ClosedAt, Now: pr.CheckedAt,
		})
		require.NoError(t, err)
	}

	tests := []struct {
		name string
		read func(t *testing.T, name string) *action.PullRequest
	}{
		{
			name: "List",
			read: func(t *testing.T, name string) *action.PullRequest {
				views, err := List(t.Context(), h.pool, ListOptions{Statuses: []action.ProposalStatus{action.StatusProposed}, PageSize: 10})
				require.NoError(t, err)

				for _, v := range views {
					if v.ID == ids[name].String() {
						return v.PullRequest
					}
				}

				require.Failf(t, "not listed", "the %s proposal is not in the list", name)

				return nil
			},
		},
		{
			name: "Get",
			read: func(t *testing.T, name string) *action.PullRequest {
				view, err := Get(t.Context(), h.pool, ids[name])
				require.NoError(t, err)

				return view.PullRequest
			},
		},
		{
			name: "OfInvestigation",
			read: func(t *testing.T, name string) *action.PullRequest {
				list, err := OfInvestigation(t.Context(), h.pool, checkoutInvestigation)
				require.NoError(t, err)

				for _, p := range list {
					if p.ID == ids[name].String() {
						return p.PullRequest
					}
				}

				require.Failf(t, "not listed", "the %s proposal is not in the investigation", name)

				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			for name, pr := range want {
				require.Equal(t, pr, tt.read(t, name), "the pull request of the %s proposal", name)
			}
		})
	}
}

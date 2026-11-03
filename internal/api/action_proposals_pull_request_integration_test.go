//go:build integration

package api_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// pullRequestBody is the pullRequest of a proposal as the API returns it.
type pullRequestBody struct {
	Repo      string     `json:"repo"`
	Number    int64      `json:"number"`
	URL       string     `json:"url"`
	State     string     `json:"state"`
	MergedAt  *time.Time `json:"mergedAt"`
	ClosedAt  *time.Time `json:"closedAt"`
	CheckedAt time.Time  `json:"checkedAt"`
}

// proposalPullRequestBody is a proposal with only its pull request.
type proposalPullRequestBody struct {
	ID          string           `json:"id"`
	PullRequest *pullRequestBody `json:"pullRequest"`
}

func TestProposalPullRequestState(t *testing.T) {
	t.Parallel()

	s, _ := setupActionProposals(t)

	checkedAt := time.Date(2026, 11, 2, 6, 0, 0, 0, time.UTC)
	mergedAt := time.Date(2026, 11, 1, 15, 30, 0, 0, time.UTC)
	closedAt := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)

	// The API does not look at the status of a proposal, so the proposals of
	// the fixture stand in for ones that opened a pull request.
	want := map[string]*pullRequestBody{
		propApproved:  {Repo: "acme/monitors", Number: 11, URL: "https://github.com/acme/monitors/pull/11", State: "open", CheckedAt: checkedAt},
		propSucceeded: {Repo: "acme/monitors", Number: 12, URL: "https://github.com/acme/monitors/pull/12", State: "merged", MergedAt: &mergedAt, CheckedAt: checkedAt},
		propRejected:  {Repo: "acme/monitors", Number: 13, URL: "https://github.com/acme/monitors/pull/13", State: "closed", ClosedAt: &closedAt, CheckedAt: checkedAt},
		propFirst:     nil,
	}

	for id, pr := range want {
		if pr == nil {
			continue
		}

		_, err := s.pool.Exec(t.Context(),
			"INSERT INTO pull_request_states (proposal_id, repo, number, url, state, merged_at, closed_at, checked_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)",
			id, pr.Repo, pr.Number, pr.URL, pr.State, pr.MergedAt, pr.ClosedAt, pr.CheckedAt)
		require.NoError(t, err)
	}

	tests := []struct {
		name string
		read func(t *testing.T) []proposalPullRequestBody
	}{
		{
			name: "the review queue",
			read: func(t *testing.T) []proposalPullRequestBody {
				rec := s.doWithToken(t, http.MethodGet, actionProposalsRoute+"?status=proposed&status=approved&status=succeeded&status=rejected", approverToken, nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				var body struct {
					Proposals []proposalPullRequestBody `json:"proposals"`
				}

				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

				return body.Proposals
			},
		},
		{
			name: "the investigation",
			read: func(t *testing.T) []proposalPullRequestBody {
				rec := s.do(t, http.MethodGet, "/api/v1/investigations/"+propInvestgn, viSession, viCSRF, nil)
				require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

				var body struct {
					Proposals []proposalPullRequestBody `json:"proposals"`
				}

				require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))

				return body.Proposals
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := map[string]*pullRequestBody{}

			for _, p := range tt.read(t) {
				if p.PullRequest != nil {
					p.PullRequest.CheckedAt = p.PullRequest.CheckedAt.UTC()
					p.PullRequest.MergedAt = utcOrNil(p.PullRequest.MergedAt)
					p.PullRequest.ClosedAt = utcOrNil(p.PullRequest.ClosedAt)
				}

				got[p.ID] = p.PullRequest
			}

			for id, pr := range want {
				found, ok := got[id]
				require.True(t, ok, "proposal %s is returned", id)
				require.Equal(t, pr, found, "the pull request of proposal %s", id)
			}
		})
	}
}

// utcOrNil returns t in UTC, or nil for nil.
func utcOrNil(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}

	utc := t.UTC()

	return &utc
}

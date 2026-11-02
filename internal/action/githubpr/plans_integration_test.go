//go:build integration

package githubpr

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each query of the status job over
// a busy install, and checks that it uses the index it is meant to. Run
// with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	var proposal uuid.UUID

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT proposal_id FROM pull_request_states LIMIT 1").Scan(&proposal))

	tests := []struct {
		name string
		// run calls the query through q, which explains it instead of
		// running it.
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
		// notWant are texts the plan must not contain.
		notWant []string
	}{
		{
			name: "ListPullRequestsToCheck",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListPullRequestsToCheck(ctx, db.ListPullRequestsToCheckParams{
					Actions: []string{"monitor-pr"}, Since: time.Now().Add(-trackedFor), Before: time.Now(), Batch: maxChecksPerRun,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"action_proposals_status_proposed"},
			notWant:   []string{"Seq Scan on action_proposals"},
		},
		{
			name: "UpsertPullRequestState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpsertPullRequestState(ctx, db.UpsertPullRequestStateParams{
					ProposalID: proposal, Repo: "acme/monitors", Number: 7, Url: "https://github.com/acme/monitors/pull/7", State: StateOpen,
					Now: time.Now(), JobID: 1, ClaimToken: 1,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"pull_request_states_pkey", "jobs_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "GetPullRequestState",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetPullRequestState(ctx, proposal)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"pull_request_states_pkey"},
			notWant:   []string{"Seq Scan"},
		},
		{
			name: "PullRequestJobExists",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.PullRequestJobExists(ctx, uniqueKeyPrefix+time.Now().UTC().Format(time.DateOnly))

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"jobs_check_pull_requests_key"},
			notWant:   []string{"Seq Scan"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			for _, text := range tt.notWant {
				require.NotContains(t, explain.Plan, text)
			}
		})
	}
}

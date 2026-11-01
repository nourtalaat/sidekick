//go:build integration

package proposals_test

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

// planPage is the page size of the proposal list.
const planPage = 51

// planWindow is the window of the trigger's count.
const planWindow = 14 * 24 * time.Hour

// TestQueryPlans runs EXPLAIN ANALYZE on the proposal queries over 20,000
// investigations with as many ratings, 2,000 proposals and a gate of 130
// runs, and checks that each query uses the index it is meant to. Run with
// -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	var proposal uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('prop' || 1)::uuid").Scan(&proposal))

	strategy, version := "strategy-3", "v3"
	status := "passed"
	since := time.Now().Add(-planWindow)

	var cursorAt time.Time

	var cursorID uuid.UUID

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT created_at, id FROM strategy_proposals ORDER BY created_at DESC, id DESC OFFSET 500 LIMIT 1").Scan(&cursorAt, &cursorID))

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "CountWrongFeedbackForVersion",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CountWrongFeedbackForVersion(ctx, db.CountWrongFeedbackForVersionParams{Since: since, Strategy: &strategy, StrategyVersion: &version})

				return explained(err)
			},
			wantIndex: []string{"feedback_wrong_recent", "investigations_pkey"},
		},
		{
			name: "ListWrongFeedbackForVersion",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListWrongFeedbackForVersion(ctx, db.ListWrongFeedbackForVersionParams{Since: since, Strategy: &strategy, StrategyVersion: &version, MaxRows: 50})

				return explained(err)
			},
			wantIndex: []string{"feedback_wrong_recent", "investigations_pkey"},
		},
		{
			name: "ListConfirmedExamples",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListConfirmedExamples(ctx, db.ListConfirmedExamplesParams{Strategy: &strategy, MaxRows: 10})

				return explained(err)
			},
			wantIndex: []string{"investigations_strategy_confirmed"},
		},
		{
			name: "ListLessonsOfStrategy",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListLessonsOfStrategy(ctx, db.ListLessonsOfStrategyParams{Strategy: strategy, MaxRows: 10})

				return explained(err)
			},
			wantIndex: []string{"lessons_active_strategy"},
		},
		{
			name: "InsertStrategyProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertStrategyProposal(ctx, db.InsertStrategyProposalParams{ID: uuid.New(), Name: strategy, BaseVersion: "v3", TriggerCount: 3})

				return explained(err)
			},
			wantIndex: []string{"strategy_proposals_version"},
		},
		{
			name: "GetStrategyProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetStrategyProposal(ctx, proposal)

				return explained(err)
			},
			wantIndex: []string{"strategy_proposals_pkey"},
		},
		{
			name: "LockStrategyProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockStrategyProposal(ctx, proposal)

				return explained(err)
			},
			wantIndex: []string{"strategy_proposals_pkey"},
		},
		{
			name: "LockPassedStrategyProposal",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockPassedStrategyProposal(ctx, proposal)

				return explained(err)
			},
			wantIndex: []string{"strategy_proposals_pkey"},
		},
		{
			name: "ListStrategyProposalsWithAStatus",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListStrategyProposals(ctx, db.ListStrategyProposalsParams{Status: &status, PageSize: planPage})

				return explained(err)
			},
			wantIndex: []string{"strategy_proposals_"},
		},
		{
			name: "ListStrategyProposalsWithACursor",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListStrategyProposals(ctx, db.ListStrategyProposalsParams{CursorAt: &cursorAt, CursorID: &cursorID, PageSize: planPage})

				return explained(err)
			},
			wantIndex: []string{"strategy_proposals_created"},
		},
		{
			name: "ListStrategyGateRuns",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListStrategyGateRuns(ctx, proposal)

				return explained(err)
			},
			wantIndex: []string{"strategy_gate_runs_pkey"},
		},
		{
			name: "DeleteStrategyGateRuns",
			run: func(ctx context.Context, q *db.Queries) error {
				return explained(q.DeleteStrategyGateRuns(ctx, proposal))
			},
			wantIndex: []string{"strategy_gate_runs_pkey"},
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

			require.NotContains(t, explain.Plan, "Seq Scan on strategy_proposals", "the proposals are read through an index")
			require.NotContains(t, explain.Plan, "Seq Scan on strategy_gate_runs", "the kept runs are read through their key")
		})
	}
}

// explained wraps the error a query returns when the explainer ends it.
func explained(err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("run query: %w", err)
}

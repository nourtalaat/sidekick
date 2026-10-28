//go:build integration

package feedback

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

// TestQueryPlans runs EXPLAIN ANALYZE on each feedback query over 20,000
// investigations with 100,000 ratings, and checks that it uses the index
// it is meant to. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	// busy is one of the seeded investigations, in the middle of the table.
	var busy uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('inv' || 10000)::uuid").Scan(&busy))

	now := time.Now()

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "LockInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockInvestigation(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "UpsertSlackFeedback",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpsertSlackFeedback(ctx, db.UpsertSlackFeedbackParams{
					ID: uuid.New(), InvestigationID: busy, TargetKind: TargetVerdict, TargetRef: VerdictRef(1),
					Rating: "wrong", SlackUserID: "U01",
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on feedback", "Conflict Arbiter Indexes: feedback_slack_user_target"},
		},
		{
			name: "SetInvestigationConfirmation",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.SetInvestigationConfirmation(ctx, db.SetInvestigationConfirmationParams{
					ConfirmedVerdict: new("true_positive"), ConfirmedBySlackUser: new("U01"), ConfirmedAt: &now, ID: busy,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "ListTargetFeedback",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListTargetFeedback(ctx, db.ListTargetFeedbackParams{
					InvestigationID: busy, TargetKind: TargetVerdict, TargetRef: VerdictRef(1),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"feedback_"},
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

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}

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
// investigations with 140,000 ratings and 50,000 steps, and checks that it
// uses the index it is meant to. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	// busy is one of the seeded investigations, in the middle of the table.
	var busy uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('inv' || 10000)::uuid").Scan(&busy))

	// ana is a signed-in user who rated the busy investigation, and call is
	// one of its tool calls.
	var ana uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('user' || 1)::uuid").Scan(&ana))

	call := "call-10000-7"

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
			name: "GetUserFeedbackRating",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetUserFeedbackRating(ctx, db.GetUserFeedbackRatingParams{
					InvestigationID: busy, TargetKind: TargetVerdict, TargetRef: VerdictRef(1), UserID: &ana,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"feedback_user_target"},
		},
		{
			name: "UpsertUserFeedback",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpsertUserFeedback(ctx, db.UpsertUserFeedbackParams{
					ID: uuid.New(), InvestigationID: busy, TargetKind: TargetFinding, TargetRef: FindingRef(1, "f1"),
					Rating: "wrong", Reason: new("No."), UserID: &ana, Channel: ChannelUI,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on feedback", "Conflict Arbiter Indexes: feedback_user_target"},
		},
		{
			name: "GetSlackFeedbackRating",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetSlackFeedbackRating(ctx, db.GetSlackFeedbackRatingParams{
					InvestigationID: busy, TargetKind: TargetVerdict, TargetRef: VerdictRef(1), SlackUserID: "U01",
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"feedback_slack_user_target"},
		},
		{
			name: "UpsertSlackFeedbackDetails",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.UpsertSlackFeedbackDetails(ctx, db.UpsertSlackFeedbackDetailsParams{
					ID: uuid.New(), InvestigationID: busy, TargetKind: TargetFinding, TargetRef: FindingRef(1, "f1"),
					Rating: "wrong", Reason: new("No."), SlackUserID: "U01", Channel: ChannelSlack,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on feedback", "Conflict Arbiter Indexes: feedback_slack_user_target"},
		},
		{
			name: "ToolCallExists",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ToolCallExists(ctx, db.ToolCallExistsParams{InvestigationID: busy, ToolCallID: &call})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"steps_pkey"},
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

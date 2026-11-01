//go:build integration

package lessons

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each lessons query over 20,000
// lessons, 5,040 of them active, and checks that it uses the index it is
// meant to. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	// ids are lessons that a prompt used.
	var ids []uuid.UUID

	rows, err := pool.Query(t.Context(), "SELECT md5('lesson' || i)::uuid FROM generate_series(5000, 5008) AS i")
	require.NoError(t, err)

	for rows.Next() {
		var id uuid.UUID

		require.NoError(t, rows.Scan(&id))

		ids = append(ids, id)
	}

	require.NoError(t, rows.Err())

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "ListLessonCandidates",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListLessonCandidates(ctx, db.ListLessonCandidatesParams{
					MaxRows: maxCandidates, Text: rankingText, Service: "svc-7", AlertName: "alert-9", Route: "route-3", Strategy: "strategy-2",
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{
				"lessons_active_service", "lessons_active_alert", "lessons_active_route", "lessons_active_strategy", "lessons_active_global",
			},
		},
		{
			name: "GetFeedbackToDraft",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetFeedbackToDraft(ctx, md5UUID(t, pool, "fb100"))

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"feedback_pkey"},
		},
		{
			name: "LessonExistsForFeedback",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LessonExistsForFeedback(ctx, md5UUID(t, pool, "fb6000"))

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"lessons_feedback"},
		},
		{
			name: "GetToolCallStep",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetToolCallStep(ctx, db.GetToolCallStepParams{InvestigationID: md5UUID(t, pool, "inv500"), ToolCallID: new("call-500-7")})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"steps_"},
		},
		{
			name: "InsertDraftedLesson",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertDraftedLesson(ctx, db.InsertDraftedLessonParams{
					ID: uuid.New(), Text: "Check the deploys.", ScopeService: new("checkout-api"), Status: "proposed", FeedbackID: md5UUID(t, pool, "fb100"),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on lessons", "lessons_feedback"},
		},
		{
			name: "InsertLessonUses",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.InsertLessonUses(ctx, db.InsertLessonUsesParams{LessonIds: ids, InvestigationID: finalInvestigation}))
			},
			wantIndex: []string{"Insert on lesson_uses", "Conflict Arbiter Indexes: lesson_uses_pkey"},
		},
		{
			name: "TouchLessonsUsed",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.TouchLessonsUsed(ctx, ids))
			},
			wantIndex: []string{"lessons_pkey"},
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

// md5UUID returns the UUID md5(text) of the seeded rows.
func md5UUID(t *testing.T, pool *pgxpool.Pool, text string) uuid.UUID {
	t.Helper()

	var id uuid.UUID

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5($1)::uuid", text).Scan(&id))

	return id
}

//go:build integration

package lessons

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
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
					MaxRows: maxCandidates, Text: eventText, Service: "svc-7", AlertName: "alert-9", Route: "route-3", Strategy: "strategy-2",
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{
				"lessons_active_service", "lessons_active_alert", "lessons_active_route", "lessons_active_strategy", "lessons_active_global",
			},
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

//go:build integration

package backfill

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each backfill query over a
// database with 100,000 investigations to fill, and checks that it finds
// them through investigations_unlisted and scans no table whole. Run with
// -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
		// allowSeqScan is set for a query that stops at its first row,
		// which on this database, where nearly every row matches, a scan
		// finds as fast as the index. Once the job is done no row
		// matches, the statistics say so, and the partial index is empty.
		allowSeqScan bool
	}{
		{
			name: "BackfillInvestigationList",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.BackfillInvestigationList(ctx, DefaultBatchSize)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_unlisted", "Tid Scan on investigations", "events_group", "event_groups_pkey", "steps_pkey"},
		},
		{
			name: "AnyUnlistedInvestigation",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.AnyUnlistedInvestigation(ctx)

				return fmt.Errorf("run query: %w", err)
			},
			allowSeqScan: true,
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

			if !tt.allowSeqScan {
				require.NotContains(t, explain.Plan, "Seq Scan")
			}
		})
	}
}

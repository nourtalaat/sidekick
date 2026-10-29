//go:build integration

package rollup

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	// planDay is how many hours a typical run counts again: the buckets
	// that a day of changes marks.
	planDay = 24
	// planWeek is how many days a typical run counts again.
	planWeek = 7
)

// TestQueryPlans runs EXPLAIN ANALYZE on each query of the rollup job over
// a year of investigations, and checks that none reads a table whole except
// the small ones. The recounts find the investigations of a bucket through
// the index of the investigation list, and their events, watchers and
// ratings through the foreign key indexes. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	hours := bucketsOf(time.Hour, planDay)
	days := bucketsOf(24*time.Hour, planWeek)

	tests := []struct {
		name string
		// run calls the query through q, which explains it instead of
		// running it.
		run func(ctx context.Context, q *db.Queries) error
		// before runs first, on the transaction itself, to clear the rows
		// the query adds.
		before    func(ctx context.Context, t *testing.T, q *db.Queries)
		wantIndex []string
	}{
		{
			name: "TakeDirtyBuckets",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.TakeDirtyBuckets(ctx, db.TakeDirtyBucketsParams{TableName: TableStatsHourly, Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"rollup_dirty_pkey"},
		},
		{
			name: "RecountStatsHourly",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.RecountStatsHourly(ctx, hours))
			},
			before: func(ctx context.Context, t *testing.T, q *db.Queries) {
				require.NoError(t, q.DeleteStatsHourly(ctx, hours))
			},
			wantIndex: []string{"investigations_list", "events_group", "watchers_group_name"},
		},
		{
			name: "RecountQualityDaily",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.RecountQualityDaily(ctx, days))
			},
			before: func(ctx context.Context, t *testing.T, q *db.Queries) {
				require.NoError(t, q.DeleteQualityDaily(ctx, days))
			},
			wantIndex: []string{"investigations_list", "feedback_investigation"},
		},
		{
			name: "DeleteStatsHourly",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.DeleteStatsHourly(ctx, hours))
			},
			wantIndex: []string{"stats_hourly_pkey"},
		},
		{
			name: "DeleteQualityDaily",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.DeleteQualityDaily(ctx, days))
			},
			wantIndex: []string{"quality_daily_pkey"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			if tt.before != nil {
				tt.before(t.Context(), t, db.New(tx))
			}

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			for _, table := range []string{"investigations", "events", "watchers", "feedback"} {
				require.NotContains(t, explain.Plan, "Seq Scan on "+table)
			}
		})
	}
}

// bucketsOf returns n buckets of the width, the latest ending now, starting
// at a multiple of the width in UTC.
func bucketsOf(width time.Duration, n int) []time.Time {
	latest := time.Now().UTC().Truncate(width)
	out := make([]time.Time, 0, n)

	for i := range n {
		out = append(out, latest.Add(-time.Duration(i+100)*width))
	}

	return out
}

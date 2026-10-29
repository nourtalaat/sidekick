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

// TestDashboardQueryPlans runs EXPLAIN ANALYZE on each query that feeds the
// dashboard metrics over two years of rollups, for the last 30 days and for
// today. Each must use the primary key of its table and read no table
// whole. Run with -v to see the plans.
func TestDashboardQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/dashboard_plans.sql")

	window := windowAt(time.Now())

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex string
	}{
		{
			name: "DashboardQuality",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DashboardQuality(ctx, db.DashboardQualityParams{FromDay: pgDate(window.fromDay), ToDay: pgDate(window.toDay)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "quality_daily_pkey",
		},
		{
			name: "DashboardStatsByRoute",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DashboardStatsByRoute(ctx, db.DashboardStatsByRouteParams{FromHour: window.fromHour, ToHour: window.toHour})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "stats_hourly_pkey",
		},
		{
			name: "DashboardFirstFindingHistograms",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DashboardFirstFindingHistograms(ctx, db.DashboardFirstFindingHistogramsParams{FromHour: window.fromHour, ToHour: window.toHour})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "stats_hourly_pkey",
		},
		{
			name: "DashboardSpendByDay",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.DashboardSpendByDay(ctx, pgDate(window.toDay))

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "spend_daily_pkey",
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
			require.Contains(t, explain.Plan, tt.wantIndex)
			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}

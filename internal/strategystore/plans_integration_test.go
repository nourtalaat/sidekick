//go:build integration

package strategystore_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// planPage is the page size the version list asks for.
const planPage = 51

// TestQueryPlans runs EXPLAIN ANALYZE on the strategy queries over 100
// strategies with 200 versions each, and checks that each one reads
// through the primary key of its table and never scans the versions whole.
// Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	tests := []struct {
		name      string
		run       func(q *db.Queries) error
		wantIndex string
	}{
		{
			name:      "CurrentVersions",
			run:       func(q *db.Queries) error { _, err := q.ListCurrentStrategies(t.Context()); return err },
			wantIndex: "strategy_versions_pkey",
		},
		{
			name: "VersionsFirstPage",
			run: func(q *db.Queries) error {
				_, err := q.ListStrategyVersions(t.Context(), db.ListStrategyVersionsParams{Name: "strategy-7", PageSize: planPage})

				return err
			},
			wantIndex: "strategy_versions_pkey",
		},
		{
			name: "VersionsWithACursor",
			run: func(q *db.Queries) error {
				before := int32(120)
				_, err := q.ListStrategyVersions(t.Context(), db.ListStrategyVersionsParams{Name: "strategy-7", Before: &before, PageSize: planPage})

				return err
			},
			wantIndex: "strategy_versions_pkey",
		},
		{
			name: "OneVersion",
			run: func(q *db.Queries) error {
				_, err := q.GetStrategyVersion(t.Context(), db.GetStrategyVersionParams{Name: "strategy-7", Version: 42})

				return err
			},
			wantIndex: "strategy_versions_pkey",
		},
		{
			name:      "LockOneStrategy",
			run:       func(q *db.Queries) error { _, err := q.LockStrategy(t.Context(), "strategy-7"); return err },
			wantIndex: "strategies_pkey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(db.New(explain)), storetest.ErrExplained)
			require.Contains(t, explain.Plan, tt.wantIndex)
			require.NotContains(t, explain.Plan, "Seq Scan on strategy_versions")
		})
	}
}

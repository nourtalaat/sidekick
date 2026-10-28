//go:build integration

package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestQueryPlans runs EXPLAIN ANALYZE on each benchmark query and checks
// its plan. Both are plain inserts, run once per benchmark run and once per
// run of a scenario. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	started := time.Date(2026, 10, 28, 9, 0, 0, 0, time.UTC)

	// runParams is a bench_runs row with id.
	runParams := func(id uuid.UUID) db.InsertBenchRunParams {
		return db.InsertBenchRunParams{
			ID: id, StartedAt: started, FinishedAt: started.Add(time.Minute),
			SidekickVersion: "v0.1.0", GitSha: "abc1234", ConfigHash: "c0ffee",
			Connectors: json.RawMessage(`{}`), Strategies: json.RawMessage(`{}`), ScenarioSet: "bench/scenarios/public",
			Repeats: 3, Summary: json.RawMessage(`{}`), Status: statusPassed, TriggeredBy: "cli",
		}
	}

	// resultRunID is the run the explained bench_results row belongs to.
	resultRunID := ids.New()

	tests := []struct {
		name string
		// seed runs first, outside the explained query.
		seed      func(ctx context.Context, q *db.Queries) error
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "InsertBenchRun",
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.InsertBenchRun(ctx, runParams(ids.New())))
			},
			wantIndex: []string{"Insert on bench_runs"},
		},
		{
			name: "InsertBenchResult",
			seed: func(ctx context.Context, q *db.Queries) error {
				if err := q.InsertBenchRun(ctx, runParams(resultRunID)); err != nil {
					return fmt.Errorf("insert the run: %w", err)
				}

				return nil
			},
			run: func(ctx context.Context, q *db.Queries) error {
				return fmt.Errorf("run query: %w", q.InsertBenchResult(ctx, db.InsertBenchResultParams{
					RunID: resultRunID, ScenarioID: "checkout-pool-size", Repeat: 1, ExpectedVerdict: "true_positive",
					VerdictCorrect: true, CostMicros: 21500, DurationMs: 4100, InputTokens: 3000, OutputTokens: 600, ToolCalls: 4,
				}))
			},
			wantIndex: []string{"Insert on bench_results"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			if tt.seed != nil {
				require.NoError(t, tt.seed(t.Context(), db.New(tx)))
			}

			explain := storetest.NewExplainer(t, tx)

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain)), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}

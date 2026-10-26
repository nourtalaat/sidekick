//go:build integration

package queue

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// planBatch is the claim batch size in the query plan tests.
const (
	planBatch = 8
	// planGlobalLimit is above the 500 investigations running in the busy
	// queue, so the limited claim can take one.
	planGlobalLimit = 1000
)

// TestQueryPlans runs EXPLAIN ANALYZE on each queue query over a busy queue
// with more than 100,000 ready jobs, and checks that it uses the index it
// is meant to. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := setupBusyQueue(t)

	tests := []struct {
		name string
		// run calls the query through q, which explains it instead of
		// running it.
		run       func(ctx context.Context, q *db.Queries, rows planRows) error
		wantIndex string
	}{
		{
			name: "ClaimJobs",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.ClaimJobs(ctx, db.ClaimJobsParams{
					Worker: testWorker, LeaseSeconds: testLease.Seconds(), Kinds: []string{KindPublish}, Batch: planBatch,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_ready",
		},
		{
			name: "ClaimLimitedJob",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.ClaimLimitedJob(ctx, db.ClaimLimitedJobParams{
					Worker: testWorker, LeaseSeconds: testLease.Seconds(), LimitedKinds: limitedKinds,
					Routes: []string{"route-0", "route-1"}, RouteLimits: []int32{50, 100},
					Kind: KindInvestigate, GlobalLimit: planGlobalLimit,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_ready",
		},
		{
			name: "CompleteJob",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.CompleteJob(ctx, db.CompleteJobParams{ID: rows.claimed.ID, ClaimToken: ClaimToken(rows.claimed)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_pkey",
		},
		{
			name: "FailJob",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.FailJob(ctx, db.FailJobParams{
					LastError: "boom", BackoffSeconds: 5, ID: rows.claimed.ID, ClaimToken: ClaimToken(rows.claimed),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_pkey",
		},
		{
			name: "HandBackJob",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.HandBackJob(ctx, db.HandBackJobParams{ID: rows.claimed.ID, ClaimToken: ClaimToken(rows.claimed)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_pkey",
		},
		{
			name: "ReleaseUniqueKey",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.ReleaseUniqueKey(ctx, db.ReleaseUniqueKeyParams{ID: rows.claimed.ID, ClaimToken: ClaimToken(rows.claimed)})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_pkey",
		},
		{
			name: "RenewLease",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.RenewLease(ctx, db.RenewLeaseParams{
					LeaseSeconds: testLease.Seconds(), ID: rows.claimed.ID, ClaimToken: ClaimToken(rows.claimed),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_pkey",
		},
		{
			name: "WaitForJobWriters",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				err := q.WaitForJobWriters(ctx, []int64{rows.claimed.ID})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_pkey",
		},
		{
			name: "ReapExpiredLeases",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.ReapExpiredLeases(ctx)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_leases",
		},
		{
			name: "CountJobs",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.CountJobs(ctx)

				return fmt.Errorf("run query: %w", err)
			},
			// It reads every job that is not done, which takes a scan.
			wantIndex: "Seq Scan on jobs",
		},
		{
			name: "EnqueueJob",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.EnqueueJob(ctx, db.EnqueueJobParams{
					Kind: KindInvestigate, Payload: []byte(`{}`), UniqueKey: new("investigate:plan"),
					Priority: DefaultPriority, MaxAttempts: DefaultMaxAttempts,
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_active_key",
		},
		{
			name: "RetryDeadJob",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.RetryDeadJob(ctx, rows.deadID)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: "jobs_pkey",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			rows := findPlanRows(t, tx)
			explain := &storetest.Explainer{Tx: tx}

			require.ErrorIs(t, tt.run(t.Context(), db.New(explain), rows), storetest.ErrExplained)
			t.Logf("EXPLAIN ANALYZE of %s:\n%s", tt.name, explain.Plan)
			require.Contains(t, explain.Plan, tt.wantIndex)
		})
	}
}

// planRows are rows of the busy queue that the queries in TestQueryPlans
// work on.
type planRows struct {
	// claimed is a job that the subtest's transaction claimed.
	claimed db.Job
	deadID  int64
}

// findPlanRows claims a job in tx and finds a dead job of the busy queue.
func findPlanRows(t *testing.T, tx pgx.Tx) planRows {
	t.Helper()

	jobs, err := claim(t.Context(), tx, testWorker, KindRouteEvent, testLease, 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	var deadID int64

	err = tx.QueryRow(t.Context(), "SELECT id FROM jobs WHERE status = 'dead' LIMIT 1").Scan(&deadID)
	require.NoError(t, err)

	return planRows{claimed: jobs[0], deadID: deadID}
}

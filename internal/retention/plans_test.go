//go:build integration

package retention

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The periods of the query plan tests: the defaults of the config.
const (
	planEvents         = 90 * 24 * time.Hour
	planInvestigations = 365 * 24 * time.Hour
	planSteps          = 90 * 24 * time.Hour
	planAuditLog       = 730 * 24 * time.Hour
	planWatcherChecks  = 30 * 24 * time.Hour
	planMemoryEntries  = 730 * 24 * time.Hour
	// planBatchIDs is how many investigations the queries that take IDs
	// get.
	planBatchIDs = 100
)

// TestQueryPlans runs EXPLAIN ANALYZE on each retention query over a
// database with 1,000,000 steps, and checks that it uses the index it is
// meant to and scans no table whole. The deletes by age find their rows
// through the index and delete them with a TID scan. Run with -v to see
// the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/plans.sql")

	before := func(period time.Duration) time.Time { return time.Now().Add(-period) }

	tests := []struct {
		name string
		// run calls the query through q, which explains it instead of
		// running it.
		run       func(ctx context.Context, q *db.Queries, rows planRows) error
		wantIndex []string
	}{
		{
			name: "DeleteOldEvents",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.DeleteOldEvents(ctx, db.DeleteOldEventsParams{Before: before(planEvents), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"events_received", "Tid Scan on events"},
		},
		{
			name: "DeleteOldSteps",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.DeleteOldSteps(ctx, db.DeleteOldStepsParams{Before: before(planSteps), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"steps_started", "Tid Scan on steps"},
		},
		{
			name: "LockOldInvestigations",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.LockOldInvestigations(ctx, db.LockOldInvestigationsParams{Before: before(planInvestigations), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_created", "event_groups_pkey"},
		},
		{
			name: "DeleteWatchersOf",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.DeleteWatchersOf(ctx, db.DeleteWatchersOfParams{InvestigationIds: rows.ids, GroupIds: rows.groupIDs})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_investigation", "watchers_group_name"},
		},
		{
			name: "DeleteInvestigations",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.DeleteInvestigations(ctx, rows.ids)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"investigations_pkey"},
		},
		{
			name: "DeleteUnusedEventGroups",
			run: func(ctx context.Context, q *db.Queries, rows planRows) error {
				_, err := q.DeleteUnusedEventGroups(ctx, rows.groupIDs)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"event_groups_pkey", "investigations_group", "watchers_group_name"},
		},
		{
			name: "DeleteOldWatcherChecks",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.DeleteOldWatcherChecks(ctx, db.DeleteOldWatcherChecksParams{Before: before(planWatcherChecks), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watcher_checks_at", "Tid Scan on watcher_checks"},
		},
		{
			name: "DeleteOldAuditEntries",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.DeleteOldAuditEntries(ctx, db.DeleteOldAuditEntriesParams{Before: before(planAuditLog), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"audit_log_at", "Tid Scan on audit_log"},
		},
		{
			name: "DeleteOldMemoryEntries",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.DeleteOldMemoryEntries(ctx, db.DeleteOldMemoryEntriesParams{Before: before(planMemoryEntries), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"memory_entries_confirmed", "Tid Scan on memory_entries"},
		},
		{
			name: "DeleteDoneJobs",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.DeleteDoneJobs(ctx, db.DeleteDoneJobsParams{Before: before(DoneJobsPeriod), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"jobs_finished", "Tid Scan on jobs"},
		},
		{
			name: "DeleteDeadJobs",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.DeleteDeadJobs(ctx, db.DeleteDeadJobsParams{Before: before(DeadJobsPeriod), Batch: DefaultBatchSize})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"jobs_finished", "Tid Scan on jobs"},
		},
		{
			name: "RetentionJobExists",
			run: func(ctx context.Context, q *db.Queries, _ planRows) error {
				_, err := q.RetentionJobExists(ctx, uniqueKeyPrefix+time.Now().UTC().Format(time.DateOnly))

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"jobs_retention_key"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			defer func() { _ = tx.Rollback(t.Context()) }()

			rows := findPlanRows(t, tx)
			explain := storetest.NewExplainer(t, tx)

			require.NoError(t, db.New(tx).AllowAuditLogDelete(t.Context()))
			require.ErrorIs(t, tt.run(t.Context(), db.New(explain), rows), storetest.ErrExplained)

			for _, index := range tt.wantIndex {
				require.Contains(t, explain.Plan, index)
			}

			require.NotContains(t, explain.Plan, "Seq Scan")
		})
	}
}

// planRows are the oldest investigations and their groups, for the
// queries that take IDs.
type planRows struct {
	ids, groupIDs []uuid.UUID
}

// findPlanRows reads the planBatchIDs oldest investigations in tx.
func findPlanRows(t *testing.T, tx pgx.Tx) planRows {
	t.Helper()

	rows, err := tx.Query(t.Context(), "SELECT id, group_id FROM investigations ORDER BY created_at LIMIT $1", planBatchIDs)
	require.NoError(t, err)

	var out planRows

	for rows.Next() {
		var id, group uuid.UUID
		require.NoError(t, rows.Scan(&id, &group))

		out.ids = append(out.ids, id)
		out.groupIDs = append(out.groupIDs, group)
	}

	require.NoError(t, rows.Err())

	return out
}

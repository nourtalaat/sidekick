//go:build integration

package watch

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

// TestQueryPlans runs EXPLAIN ANALYZE on each watcher query over 50,000
// watchers in 5,000 groups and checks that it uses the index it is meant
// to. Run with -v to see the plans.
func TestQueryPlans(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy.sql")

	// busy is a group in the middle of the table, and busyWatcher one of
	// its watchers.
	var busy, busyWatcher uuid.UUID
	require.NoError(t, pool.QueryRow(t.Context(), "SELECT md5('group' || 2500)::uuid, md5('watcher' || 2500 || '-' || 1)::uuid").Scan(&busy, &busyWatcher))

	now := time.Now()

	tests := []struct {
		name      string
		run       func(ctx context.Context, q *db.Queries) error
		wantIndex []string
	}{
		{
			name: "CountGroupWatchers",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CountGroupWatchers(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_group_name"},
		},
		{
			name: "CountRunningWatchers",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.CountRunningWatchers(ctx, now)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_running"},
		},
		{
			name: "InsertWatcher",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.InsertWatcher(ctx, db.InsertWatcherParams{
					ID: uuid.New(), GroupID: busy, Name: "new", Origin: "model", CheckType: "alert", Spec: []byte("{}"),
					IntervalS: 30, PassesToRecover: 1, CheckTimeoutS: 10, StartedAt: now, Deadline: now.Add(time.Hour),
				})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on watchers"},
		},
		{
			name: "GetWatcherByName",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetWatcherByName(ctx, db.GetWatcherByNameParams{GroupID: busy, Name: "watcher-1"})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_group_name"},
		},
		{
			name: "GetWatcher",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetWatcher(ctx, busyWatcher)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_pkey"},
		},
		{
			name: "GetWatcherForTick",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GetWatcherForTick(ctx, busyWatcher)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_pkey", "event_groups_pkey"},
		},
		{
			name: "LockWatcher",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.LockWatcher(ctx, busyWatcher)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_pkey"},
		},
		{
			name: "GroupHasRunningWatchers",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.GroupHasRunningWatchers(ctx, db.GroupHasRunningWatchersParams{GroupID: busy, Now: now})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_group_name"},
		},
		{
			name: "RecordWatcherCheck",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.RecordWatcherCheck(ctx, db.RecordWatcherCheckParams{At: now, Passed: true, ID: busyWatcher})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_pkey"},
		},
		{
			name: "InsertWatcherCheck",
			run: func(ctx context.Context, q *db.Queries) error {
				err := q.InsertWatcherCheck(ctx, db.InsertWatcherCheckParams{WatcherID: busyWatcher, Seq: 3, At: now, Result: "pass", DurationMs: 2})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"Insert on watcher_checks"},
		},
		{
			name: "EndWatcher",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.EndWatcher(ctx, db.EndWatcherParams{Status: StatusRecovered, At: now, ID: busyWatcher})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_pkey"},
		},
		{
			name: "EndGroupWatchersResolved",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.EndGroupWatchersResolved(ctx, db.EndGroupWatchersResolvedParams{At: now, GroupID: busy})

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"watchers_group_name"},
		},
		{
			name: "ListGroupAlertStates",
			run: func(ctx context.Context, q *db.Queries) error {
				_, err := q.ListGroupAlertStates(ctx, busy)

				return fmt.Errorf("run query: %w", err)
			},
			wantIndex: []string{"events_group"},
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

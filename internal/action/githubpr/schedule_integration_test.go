//go:build integration

package githubpr

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// TestSchedulerQueuesTheStatusJobOncePerDay checks the wiring of the daily
// scheduler: the job kind, the unique key, and that a job that finished
// counts as the day's job.
func TestSchedulerQueuesTheStatusJobOncePerDay(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	now := time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)
	s := NewScheduler(SchedulerOptions{Pool: pool, Now: func() time.Time { return now }})

	enqueued, err := s.Schedule(t.Context())
	require.NoError(t, err)
	require.True(t, enqueued)
	require.Equal(t, []string{"pr_status:2026-10-28"}, statusKeys(t, pool))

	_, err = pool.Exec(t.Context(), "UPDATE jobs SET status = $1, finished_at = now() WHERE kind = $2", queue.StatusDone, queue.KindCheckPullRequests)
	require.NoError(t, err)

	enqueued, err = s.Schedule(t.Context())
	require.NoError(t, err)
	require.False(t, enqueued, "a job that is done is the day's job")

	now = now.Add(24 * time.Hour)

	enqueued, err = s.Schedule(t.Context())
	require.NoError(t, err)
	require.True(t, enqueued, "the next day gets its own job")
	require.Equal(t, []string{"pr_status:2026-10-28", "pr_status:2026-10-29"}, statusKeys(t, pool))
}

// statusKeys returns the unique keys of the status jobs, in order.
func statusKeys(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT unique_key FROM jobs WHERE kind = $1 ORDER BY unique_key", queue.KindCheckPullRequests)
	require.NoError(t, err)

	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return keys
}

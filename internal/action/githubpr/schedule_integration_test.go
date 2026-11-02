//go:build integration

package githubpr

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// testReplicas is how many replicas the schedule test runs at once.
const testReplicas = 4

// testDay is the time the schedule tests start at.
var testDay = time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)

// TestScheduleOncePerDay checks that replicas scheduling at the same time
// enqueue one job for the day, and that none enqueues another once that
// job is running, done or dead, until the next day.
func TestScheduleOncePerDay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// status is what the first job of the day becomes.
		status string
	}{
		{name: "pending", status: "pending"},
		{name: "running", status: "running"},
		{name: "done", status: "done"},
		{name: "dead", status: "dead"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			now := testDay

			replicas := make([]*Scheduler, testReplicas)
			for i := range replicas {
				replicas[i] = NewScheduler(SchedulerOptions{Pool: pool, Now: func() time.Time { return now }})
			}

			require.Equal(t, 1, scheduleAll(t, replicas), "one replica enqueues the job")
			require.Equal(t, []string{"pr_status:2026-10-28"}, statusKeys(t, pool))

			_, err := pool.Exec(t.Context(), "UPDATE jobs SET status = $1, finished_at = now() WHERE kind = 'check_pull_requests'", tt.status)
			require.NoError(t, err)

			require.Zero(t, scheduleAll(t, replicas), "the day has its job")

			now = testDay.Add(24 * time.Hour)

			require.Equal(t, 1, scheduleAll(t, replicas), "the next day gets its own job")
			require.Equal(t, []string{"pr_status:2026-10-28", "pr_status:2026-10-29"}, statusKeys(t, pool))
		})
	}
}

// scheduleAll runs Schedule on every replica at once and returns how many
// enqueued a job.
func scheduleAll(t *testing.T, replicas []*Scheduler) int {
	t.Helper()

	var (
		wg       sync.WaitGroup
		enqueued atomic.Int32
		start    = make(chan struct{})
		errs     = make(chan error, len(replicas))
	)

	for _, s := range replicas {
		wg.Go(func() {
			<-start

			ok, err := s.Schedule(t.Context())
			if err != nil {
				errs <- err

				return
			}

			if ok {
				enqueued.Add(1)
			}
		})
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}

	return int(enqueued.Load())
}

// statusKeys returns the unique keys of the status jobs, oldest first.
func statusKeys(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT unique_key FROM jobs WHERE kind = 'check_pull_requests' ORDER BY unique_key")
	require.NoError(t, err)

	defer rows.Close()

	var keys []string

	for rows.Next() {
		var key string

		require.NoError(t, rows.Scan(&key))

		keys = append(keys, key)
	}

	require.NoError(t, rows.Err())

	return keys
}

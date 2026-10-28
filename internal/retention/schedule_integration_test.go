//go:build integration

package retention

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// testReplicas is how many replicas the schedule tests run at once.
const testReplicas = 4

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
		{name: "pending", status: queue.StatusPending},
		{name: "running", status: queue.StatusRunning},
		{name: "done", status: queue.StatusDone},
		{name: "dead", status: queue.StatusDead},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := storetest.New(t)
			now := testNow

			replicas := make([]*Scheduler, testReplicas)
			for i := range replicas {
				replicas[i] = NewScheduler(SchedulerOptions{Pool: pool, Now: func() time.Time { return now }})
			}

			require.Equal(t, 1, scheduleAll(t, replicas), "one replica enqueues the job")
			require.Equal(t, []string{"retention:2026-10-28"}, retentionKeys(t, pool))

			_, err := pool.Exec(t.Context(), "UPDATE jobs SET status = $1, finished_at = now() WHERE kind = 'retention'", tt.status)
			require.NoError(t, err)

			require.Zero(t, scheduleAll(t, replicas), "the day has its job")
			require.Equal(t, []string{"retention:2026-10-28"}, retentionKeys(t, pool))

			now = testNow.Add(24 * time.Hour)

			require.Equal(t, 1, scheduleAll(t, replicas), "the next day gets its own job")
			require.Equal(t, []string{"retention:2026-10-28", "retention:2026-10-29"}, retentionKeys(t, pool))
		})
	}
}

// TestTwoReplicasRunTheJobOnce runs two replicas, each with a Scheduler
// and a queue Runner, and checks that the job of the day runs once even
// when both schedule it again after it finished.
func TestTwoReplicasRunTheJobOnce(t *testing.T) {
	t.Parallel()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/records.sql")

	var runs atomic.Int32

	handler := New(Options{Pool: pool, Periods: testPeriods, Now: func() time.Time { return testNow }})
	counted := queue.HandlerFunc(func(ctx context.Context, job db.Job) error {
		runs.Add(1)

		return handler.Handle(ctx, job)
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	var wg sync.WaitGroup

	replicas := make([]*Scheduler, 2)

	for i := range replicas {
		replicas[i] = NewScheduler(SchedulerOptions{Pool: pool, Now: func() time.Time { return testNow }})

		runner, err := queue.NewRunner(pool, queue.Options{PollInterval: 20 * time.Millisecond})
		require.NoError(t, err)
		require.NoError(t, runner.Register(queue.KindRetention, counted, queue.KindOptions{Long: true}))

		wg.Go(func() { _ = runner.Run(ctx) })
	}

	scheduleAll(t, replicas)

	require.Eventually(t, func() bool {
		var done bool

		err := pool.QueryRow(t.Context(), "SELECT count(*) = 1 FROM jobs WHERE kind = 'retention' AND status = 'done'").Scan(&done)

		return err == nil && done
	}, 30*time.Second, 20*time.Millisecond)

	require.Zero(t, scheduleAll(t, replicas), "the replicas do not schedule the day again")

	cancel()
	wg.Wait()

	require.Equal(t, int32(1), runs.Load())
	require.Equal(t, []string{"retention:2026-10-28"}, retentionKeys(t, pool))
}

// scheduleAll calls Schedule on every replica at once and returns how many
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

// retentionKeys returns the unique keys of the retention jobs, in order.
func retentionKeys(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT unique_key FROM jobs WHERE kind = 'retention' ORDER BY unique_key")
	require.NoError(t, err)

	var keys []string

	for rows.Next() {
		var key string
		require.NoError(t, rows.Scan(&key))

		keys = append(keys, key)
	}

	require.NoError(t, rows.Err())

	return keys
}

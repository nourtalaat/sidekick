//go:build integration

package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const (
	// testDailyKind and testDailyPrefix name the job of the daily tests.
	testDailyKind   = "daily_test"
	testDailyPrefix = "daily_test:"
	// testDailyLock is the advisory lock key of the daily tests.
	testDailyLock int64 = 0x7365742d6461696c
	// testReplicas is how many replicas the daily tests run at once.
	testReplicas = 4
)

// testDailyStart is the time the daily tests start at.
var testDailyStart = time.Date(2026, 10, 28, 9, 30, 0, 0, time.UTC)

// errExists is what a failing Exists returns.
var errExists = errors.New("the exists query failed")

// dailyExists looks for a job of the test kind with the key, in any status.
func dailyExists(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	var found bool

	err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE kind = $1 AND unique_key = $2)", testDailyKind, key).Scan(&found)
	if err != nil {
		return false, errors.Join(errExists, err)
	}

	return found, nil
}

// newReplicas returns replicas that all read the clock now points at.
func newReplicas(pool *pgxpool.Pool, now *time.Time) []*DailyScheduler {
	replicas := make([]*DailyScheduler, testReplicas)
	for i := range replicas {
		replicas[i] = NewDailyScheduler(DailyOptions{
			Pool: pool, Kind: testDailyKind, KeyPrefix: testDailyPrefix, LockKey: testDailyLock,
			Exists: dailyExists, Now: func() time.Time { return *now },
		})
	}

	return replicas
}

// TestDailySchedulerOncePerDay checks that replicas scheduling at the same
// time enqueue one job for the day, and that none enqueues another once that
// job is running, done or dead, until the next day.
func TestDailySchedulerOncePerDay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// status is what the first job of the day becomes.
		status string
	}{
		{name: "pending", status: StatusPending},
		{name: "running", status: StatusRunning},
		{name: "done", status: StatusDone},
		{name: "dead", status: StatusDead},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			now := testDailyStart
			replicas := newReplicas(pool, &now)

			require.Equal(t, 1, scheduleAll(t, replicas), "one replica enqueues the job")
			require.Equal(t, []string{"daily_test:2026-10-28"}, dailyKeys(t, pool))

			_, err := pool.Exec(t.Context(), "UPDATE jobs SET status = $1, finished_at = now() WHERE kind = $2", tt.status, testDailyKind)
			require.NoError(t, err)

			require.Zero(t, scheduleAll(t, replicas), "the day has its job")

			now = testDailyStart.Add(24 * time.Hour)

			require.Equal(t, 1, scheduleAll(t, replicas), "the next day gets its own job")
			require.Equal(t, []string{"daily_test:2026-10-28", "daily_test:2026-10-29"}, dailyKeys(t, pool))
		})
	}
}

func TestDailySchedulerPayloadAndError(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	now := testDailyStart

	_, err := newReplicas(pool, &now)[0].Schedule(t.Context())
	require.NoError(t, err)

	var payload string

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT payload::text FROM jobs WHERE kind = $1", testDailyKind).Scan(&payload))
	require.JSONEq(t, `{"day":"2026-10-28"}`, payload)

	failing := NewDailyScheduler(DailyOptions{
		Pool: pool, Kind: testDailyKind, KeyPrefix: testDailyPrefix, LockKey: testDailyLock,
		Exists: func(context.Context, pgx.Tx, string) (bool, error) { return false, errExists },
	})

	enqueued, err := failing.Schedule(t.Context())
	require.ErrorIs(t, err, errExists)
	require.ErrorContains(t, err, "schedule the daily_test job of")
	require.False(t, enqueued)
}

// TestDailySchedulerRun checks that Run schedules the job at once and stops
// when its context ends.
func TestDailySchedulerRun(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	now := testDailyStart
	s := newReplicas(pool, &now)[0]

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	done := make(chan error, 1)

	go func() { done <- s.Run(ctx) }()

	require.Eventually(t, func() bool {
		var n int

		err := pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1", testDailyKind).Scan(&n)

		return err == nil && n == 1
	}, 30*time.Second, 20*time.Millisecond)

	cancel()
	require.NoError(t, <-done)
}

// scheduleAll calls Schedule on every replica at once and returns how many
// enqueued a job.
func scheduleAll(t *testing.T, replicas []*DailyScheduler) int {
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

// dailyKeys returns the unique keys of the daily test jobs, in order.
func dailyKeys(t *testing.T, pool *pgxpool.Pool) []string {
	t.Helper()

	rows, err := pool.Query(t.Context(), "SELECT unique_key FROM jobs WHERE kind = $1 ORDER BY unique_key", testDailyKind)
	require.NoError(t, err)

	keys, err := pgx.CollectRows(rows, pgx.RowTo[string])
	require.NoError(t, err)

	return keys
}

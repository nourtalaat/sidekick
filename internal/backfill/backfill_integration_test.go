//go:build integration

package backfill

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// testReplicas is how many replicas schedule at once.
const testReplicas = 4

// wantFilled is what the list columns of fixture/investigations.sql hold
// once the job has run.
var wantFilled = []listed{
	{ID: "c1", Title: ptr("p99 above 2s"), Service: ptr("checkout-api"), Model: ptr("big-model")},
	{ID: "c2", Title: ptr("DiskFull")},
	{ID: "c3", Title: ptr("Why is the queue backed up?"), Service: ptr("db")},
	{ID: "c4", Title: ptr(""), Service: ptr("db")},
	{ID: "c5", Title: ptr(""), Service: ptr("api")},
}

func TestHandleFillsEveryBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		batch int32
	}{
		{name: "one batch", batch: 100},
		{name: "batches of two", batch: 2},
		{name: "batches the size of the rows left", batch: 4},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			h := New(Options{Pool: pool, BatchSize: tt.batch})

			require.NoError(t, h.Handle(t.Context(), db.Job{}))
			require.Equal(t, wantFilled, columns(t, pool))

			unlisted, err := db.New(pool).AnyUnlistedInvestigation(t.Context())
			require.NoError(t, err)
			require.False(t, unlisted)

			require.NoError(t, h.Handle(t.Context(), db.Job{}), "a second run changes nothing")
			require.Equal(t, wantFilled, columns(t, pool))
		})
	}
}

func TestHandleKeepsGoingAfterAShortBatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		batch int32
	}{
		{name: "batches of two", batch: 2},
		{name: "batches of three", batch: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			// A transaction that updates the oldest unfilled row and stays open
			// makes the first batch wait on that row. When the transaction
			// commits, the batch skips the row, so it fills fewer rows than its
			// size while rows are still unfilled.
			tx, err := pool.Begin(t.Context())
			require.NoError(t, err)

			t.Cleanup(func() { _ = tx.Rollback(context.Background()) })

			_, err = tx.Exec(t.Context(), "UPDATE investigations SET status = status WHERE right(id::text, 2) = 'c1'")
			require.NoError(t, err)

			done := make(chan error, 1)

			go func() {
				done <- New(Options{Pool: pool, BatchSize: tt.batch}).Handle(t.Context(), db.Job{})
			}()

			require.Eventually(t, func() bool {
				var waiting int

				err := pool.QueryRow(t.Context(), "SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'").Scan(&waiting)

				return err == nil && waiting > 0
			}, 10*time.Second, 20*time.Millisecond, "the first batch waits on the updated row")

			require.NoError(t, tx.Commit(t.Context()))
			require.NoError(t, <-done)

			require.Equal(t, wantFilled, columns(t, pool))
		})
	}
}

func TestHandleKeepsWhatTheRouterAndInvestigatorSet(t *testing.T) {
	t.Parallel()

	pool := setup(t)

	_, err := pool.Exec(t.Context(), "UPDATE investigations SET model = 'live-model', service = 'live-service' WHERE right(id::text, 2) = 'c1'")
	require.NoError(t, err)

	require.NoError(t, New(Options{Pool: pool}).Handle(t.Context(), db.Job{}))

	got := columns(t, pool)
	require.Equal(t, ptr("live-model"), got[0].Model)
	require.Equal(t, ptr("live-service"), got[0].Service)
}

func TestScheduleEnqueuesOneJob(t *testing.T) {
	t.Parallel()

	pool := setup(t)

	replicas := make([]*Scheduler, testReplicas)
	for i := range replicas {
		replicas[i] = NewScheduler(SchedulerOptions{Pool: pool})
	}

	var enqueued atomic.Int32

	var wg sync.WaitGroup

	for _, s := range replicas {
		wg.Go(func() {
			ok, err := s.Schedule(t.Context())
			if ok {
				enqueued.Add(1)
			}

			if err != nil {
				t.Error(err)
			}
		})
	}

	wg.Wait()

	require.EqualValues(t, 1, enqueued.Load(), "one replica enqueues the job")

	var keys []string

	rows, err := pool.Query(t.Context(), "SELECT unique_key FROM jobs WHERE kind = $1", queue.KindBackfill)
	require.NoError(t, err)

	for rows.Next() {
		var key string

		require.NoError(t, rows.Scan(&key))

		keys = append(keys, key)
	}

	require.NoError(t, rows.Err())
	require.Equal(t, []string{InvestigationListKey}, keys)
}

func TestScheduleEnqueuesNothingOnceFilled(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	require.NoError(t, New(Options{Pool: pool}).Handle(t.Context(), db.Job{}))

	ok, err := NewScheduler(SchedulerOptions{Pool: pool}).Schedule(t.Context())
	require.NoError(t, err)
	require.False(t, ok)

	var jobs int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1", queue.KindBackfill).Scan(&jobs))
	require.Zero(t, jobs)
}

func TestRunReturnsAfterScheduling(t *testing.T) {
	t.Parallel()

	pool := setup(t)

	require.NoError(t, NewScheduler(SchedulerOptions{Pool: pool}).Run(t.Context()))

	var jobs int

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = $1", queue.KindBackfill).Scan(&jobs))
	require.Equal(t, 1, jobs)
}

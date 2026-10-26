//go:build integration

package queue

import (
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	// testWorker is the locked_by value of jobs that tests claim by hand.
	testWorker = "test-worker"
	// testLease is the lease of jobs that tests claim by hand, long enough
	// that it never expires during a test unless the test waits for it.
	testLease = time.Minute
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// setup returns a pool on a new, migrated database with an empty queue.
func setup(t *testing.T) *pgxpool.Pool {
	t.Helper()

	return storetest.New(t)
}

// setupBusyQueue returns a pool on a new database seeded with a busy queue
// of more than 100,000 ready jobs.
func setupBusyQueue(t *testing.T) *pgxpool.Pool {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/busy_queue.sql")

	return pool
}

// mustEnqueue enqueues job and returns its ID.
func mustEnqueue(t *testing.T, pool *pgxpool.Pool, job NewJob) int64 {
	t.Helper()

	if job.Payload == nil {
		job.Payload = []byte(`{}`)
	}

	id, err := Enqueue(t.Context(), pool, job)
	require.NoError(t, err)

	return id
}

// mustClaimOne claims exactly one ready job of kind.
func mustClaimOne(t *testing.T, pool *pgxpool.Pool, kind string) db.Job {
	t.Helper()

	jobs, err := claim(t.Context(), pool, testWorker, kind, testLease, 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	return jobs[0]
}

func getJob(t *testing.T, pool *pgxpool.Pool, id int64) db.Job {
	t.Helper()

	job, err := db.New(pool).GetJob(t.Context(), id)
	require.NoError(t, err)

	return job
}

// makeReady moves a job's run_at to now, so a test need not wait for its
// backoff.
func makeReady(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()

	_, err := pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() WHERE id = $1", id)
	require.NoError(t, err)
}

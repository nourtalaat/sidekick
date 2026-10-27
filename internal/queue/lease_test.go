//go:build integration

package queue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

const (
	// shortLease is a lease that expires almost at once.
	shortLease = 50 * time.Millisecond
	// reapWait is how long a test waits for an expired lease to be reaped.
	reapWait = 5 * time.Second
	// reapTick is how often a test that waits calls reap.
	reapTick = 20 * time.Millisecond
	// replicaMaxConns is the pool size of each simulated replica.
	replicaMaxConns = 4
)

// TestLostLeaseIsTakenOver follows a worker that stops renewing its lease:
// the reaper resets the job, a second worker claims it with a higher token,
// and the first worker's token no longer matches for any write.
func TestLostLeaseIsTakenOver(t *testing.T) {
	t.Parallel()

	url := storetest.NewURL(t)
	first, second := openReplica(t, url), openReplica(t, url)

	id := mustEnqueue(t, first, NewJob{Kind: KindInvestigate})
	stale := mustClaimOne(t, first, KindInvestigate)
	require.NoError(t, renew(t.Context(), first, stale, shortLease), "the first worker renews once, then stops")

	reaped := waitForReap(t, second)
	require.Len(t, reaped, 1)
	require.Equal(t, id, reaped[0].ID)
	require.Equal(t, StatusPending, reaped[0].Status)
	require.Equal(t, int32(1), reaped[0].Attempts, "an expired lease counts an attempt")
	require.Equal(t, new("lease expired"), reaped[0].LastError)
	require.Nil(t, reaped[0].LockedBy)

	owner := mustClaimOne(t, second, KindInvestigate)
	require.Equal(t, id, owner.ID)
	require.Greater(t, *owner.ClaimToken, *stale.ClaimToken)

	tests := []struct {
		name  string
		write func(ctx context.Context, q db.DBTX, job db.Job) error
	}{
		{name: "renew", write: func(ctx context.Context, q db.DBTX, job db.Job) error {
			return renew(ctx, q, job, testLease)
		}},
		{name: "complete", write: complete},
		{name: "fail", write: func(ctx context.Context, q db.DBTX, job db.Job) error {
			_, err := fail(ctx, q, job, errors.New("boom"), false, 0)

			return err
		}},
		{name: "give up", write: func(ctx context.Context, q db.DBTX, job db.Job) error {
			_, err := fail(ctx, q, job, errors.New("boom"), true, 0)

			return err
		}},
		{name: "hand back", write: handBack},
		{name: "release the unique key", write: ReleaseUniqueKey},
	}

	for _, tt := range tests {
		require.ErrorIs(t, tt.write(t.Context(), first, stale), ErrLeaseLost, tt.name)
	}

	job := getJob(t, first, id)
	require.Equal(t, StatusRunning, job.Status)
	require.Equal(t, owner.ClaimToken, job.ClaimToken, "the second worker still owns the job")

	require.NoError(t, complete(t.Context(), second, owner))
	require.Equal(t, StatusDone, getJob(t, first, id).Status)
}

func TestRenewKeepsTheLease(t *testing.T) {
	t.Parallel()

	pool := setup(t)
	id := mustEnqueue(t, pool, NewJob{Kind: KindInvestigate})

	jobs, err := claim(t.Context(), pool, testWorker, KindInvestigate, shortLease, 1)
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	require.NoError(t, renew(t.Context(), pool, jobs[0], testLease))
	time.Sleep(2 * shortLease)

	reaped, locked, err := reap(t.Context(), pool, reaperLockKey)
	require.NoError(t, err)
	require.True(t, locked)
	require.Empty(t, reaped)

	job := getJob(t, pool, id)
	require.Equal(t, StatusRunning, job.Status)
	require.WithinDuration(t, time.Now().Add(testLease), *job.LockedUntil, clockSlack)
}

func TestReap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		maxAttempts  int32
		wantStatus   string
		wantFinished bool
	}{
		{name: "attempts left", maxAttempts: 2, wantStatus: StatusPending},
		{name: "last attempt", maxAttempts: 1, wantStatus: StatusDead, wantFinished: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)

			live := mustEnqueue(t, pool, NewJob{Kind: KindPublish})
			mustClaimOne(t, pool, KindPublish)

			expired := mustEnqueue(t, pool, NewJob{Kind: KindPublish, MaxAttempts: tt.maxAttempts})
			_, err := claim(t.Context(), pool, testWorker, KindPublish, shortLease, 1)
			require.NoError(t, err)

			reaped := waitForReap(t, pool)
			require.Len(t, reaped, 1)
			require.Equal(t, expired, reaped[0].ID)
			require.Equal(t, tt.wantStatus, reaped[0].Status)
			require.Equal(t, tt.wantFinished, reaped[0].FinishedAt != nil)
			require.Equal(t, StatusRunning, getJob(t, pool, live).Status, "a live lease is left alone")
		})
	}
}

func TestReapRunsOnOneReplicaAtATime(t *testing.T) {
	t.Parallel()

	url := storetest.NewURL(t)
	holder, other := openReplica(t, url), openReplica(t, url)

	mustEnqueue(t, other, NewJob{Kind: KindPublish})
	_, err := claim(t.Context(), other, testWorker, KindPublish, shortLease, 1)
	require.NoError(t, err)
	time.Sleep(2 * shortLease)

	conn, err := holder.Acquire(t.Context())
	require.NoError(t, err)

	defer conn.Release()

	queries := db.New(conn)
	locked, err := queries.TryLockReaper(t.Context(), reaperLockKey)
	require.NoError(t, err)
	require.True(t, locked)

	reaped, locked, err := reap(t.Context(), other, reaperLockKey)
	require.NoError(t, err)
	require.False(t, locked, "another replica holds the reaper lock")
	require.Empty(t, reaped)

	unlocked, err := queries.UnlockReaper(t.Context(), reaperLockKey)
	require.NoError(t, err)
	require.True(t, unlocked)

	reaped, locked, err = reap(t.Context(), other, reaperLockKey)
	require.NoError(t, err)
	require.True(t, locked)
	require.Len(t, reaped, 1)

	reaped, locked, err = reap(t.Context(), holder, reaperLockKey)
	require.NoError(t, err)
	require.True(t, locked, "reap released the lock")
	require.Empty(t, reaped)
}

func TestReapLockIsKeyedBySchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// held is the schema whose reaper lock another session holds.
		held string
		// reaper is the schema of the reaper that tries the lock.
		reaper     string
		wantLocked bool
	}{
		{name: "the same schema waits", held: "", reaper: "", wantLocked: false},
		{name: "a bench schema does not wait for the default one", held: "", reaper: "sidekick_bench_a", wantLocked: true},
		{name: "the default schema does not wait for a bench one", held: "sidekick_bench_a", reaper: "", wantLocked: true},
		{name: "two bench schemas do not wait for each other", held: "sidekick_bench_a", reaper: "sidekick_bench_b", wantLocked: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			url := storetest.NewURL(t)
			holder, other := openReplica(t, url), openReplica(t, url)

			conn, err := holder.Acquire(t.Context())
			require.NoError(t, err)

			defer conn.Release()

			queries := db.New(conn)
			heldKey := store.SchemaLockKey(reaperLockKey, tt.held)

			locked, err := queries.TryLockReaper(t.Context(), heldKey)
			require.NoError(t, err)
			require.True(t, locked)

			_, locked, err = reap(t.Context(), other, store.SchemaLockKey(reaperLockKey, tt.reaper))
			require.NoError(t, err)
			require.Equal(t, tt.wantLocked, locked)

			unlocked, err := queries.UnlockReaper(t.Context(), heldKey)
			require.NoError(t, err)
			require.True(t, unlocked)
		})
	}
}

// openReplica opens a pool of its own on the database at url, as one
// replica of Sidekick would.
func openReplica(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()

	pool, err := store.Open(t.Context(), url, replicaMaxConns)
	require.NoError(t, err)
	t.Cleanup(pool.Close)

	return pool
}

// waitForReap calls reap until it changes at least one job, and returns
// those jobs.
func waitForReap(t *testing.T, pool *pgxpool.Pool) []db.Job {
	t.Helper()

	deadline := time.Now().Add(reapWait)

	for time.Now().Before(deadline) {
		reaped, locked, err := reap(t.Context(), pool, reaperLockKey)
		require.NoError(t, err)
		require.True(t, locked)

		if len(reaped) > 0 {
			return reaped
		}

		time.Sleep(reapTick)
	}

	require.FailNow(t, "no lease expired", "waited %s", reapWait)

	return nil
}

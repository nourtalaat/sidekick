package queue

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// reaperLockKey is the session advisory lock that one replica at a time
// holds while it reaps expired leases. It is "sk-reapr" as bytes.
const reaperLockKey int64 = 0x736b2d7265617072

// releaseTimeout bounds the release of the reaper lock, which runs even when
// the caller's context has ended.
const releaseTimeout = 5 * time.Second

// renew extends the lease of a claimed job to lease from now. It returns
// ErrLeaseLost when the job no longer belongs to the job's claim token.
func renew(ctx context.Context, q db.DBTX, job db.Job, lease time.Duration) error {
	rows, err := db.New(q).RenewLease(ctx, db.RenewLeaseParams{
		LeaseSeconds: lease.Seconds(),
		ID:           job.ID,
		ClaimToken:   claimToken(job),
	})
	if err != nil {
		return fmt.Errorf("renew lease of job %d: %w", job.ID, err)
	}

	if rows == 0 {
		return ErrLeaseLost
	}

	return nil
}

// reap counts an attempt for every running job whose lease expired, and
// sets it back to pending, or to dead on its last attempt. Only one replica
// reaps at a time: when another holds the reaper lock, reap does nothing and
// returns locked false. It returns the jobs it changed.
func reap(ctx context.Context, pool *pgxpool.Pool) (reaped []db.Job, locked bool, err error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("reap expired leases: acquire connection: %w", err)
	}

	defer conn.Release()

	queries := db.New(conn)

	locked, err = queries.TryLockReaper(ctx, reaperLockKey)
	if err != nil {
		return nil, false, fmt.Errorf("reap expired leases: take the reaper lock: %w", err)
	}

	if !locked {
		return nil, false, nil
	}

	defer func() {
		// The lock belongs to the connection's session. If it cannot be
		// released, closing the connection ends the session and releases it,
		// so the connection does not go back to the pool still holding it.
		releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
		defer cancel()

		unlocked, unlockErr := queries.UnlockReaper(releaseCtx, reaperLockKey)
		if unlockErr == nil && unlocked {
			return
		}

		if unlockErr == nil {
			unlockErr = errors.New("the lock was not held")
		}

		err = errors.Join(err, fmt.Errorf("release the reaper lock: %w", unlockErr))

		if closeErr := conn.Conn().Close(releaseCtx); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close the reaper connection: %w", closeErr))
		}
	}()

	reaped, err = queries.ReapExpiredLeases(ctx)
	if err != nil {
		return nil, true, fmt.Errorf("reap expired leases: %w", err)
	}

	return reaped, true, nil
}

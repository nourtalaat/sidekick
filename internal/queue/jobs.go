package queue

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// maxErrorBytes is the most of a handler's error message that a failed job
// keeps in last_error.
const maxErrorBytes = 4096

// claim leases up to batch ready jobs of kind to worker for lease.
func claim(ctx context.Context, q db.DBTX, worker, kind string, lease time.Duration, batch int32) ([]db.Job, error) {
	jobs, err := db.New(q).ClaimJobs(ctx, db.ClaimJobsParams{
		Worker:       worker,
		LeaseSeconds: lease.Seconds(),
		Kinds:        []string{kind},
		Batch:        batch,
	})
	if err != nil {
		return nil, fmt.Errorf("claim %s jobs: %w", kind, err)
	}

	return jobs, nil
}

// complete marks a claimed job done. It returns ErrLeaseLost when the job no
// longer belongs to the job's claim token.
func complete(ctx context.Context, q db.DBTX, job db.Job) error {
	rows, err := db.New(q).CompleteJob(ctx, db.CompleteJobParams{ID: job.ID, ClaimToken: claimToken(job)})
	if err != nil {
		return fmt.Errorf("complete job %d: %w", job.ID, err)
	}

	if rows == 0 {
		return ErrLeaseLost
	}

	return nil
}

// fail records a failed attempt of a claimed job, caused by cause. The job
// is retried after backoff, or becomes dead when giveUp is set or it has
// used all of its attempts. It returns the updated job, or ErrLeaseLost when
// the job no longer belongs to the job's claim token.
func fail(ctx context.Context, q db.DBTX, job db.Job, cause error, giveUp bool, backoff time.Duration) (db.Job, error) {
	failed, err := db.New(q).FailJob(ctx, db.FailJobParams{
		LastError:      errorText(cause),
		GiveUp:         giveUp,
		BackoffSeconds: backoff.Seconds(),
		ID:             job.ID,
		ClaimToken:     claimToken(job),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Job{}, ErrLeaseLost
	}

	if err != nil {
		return db.Job{}, fmt.Errorf("fail job %d: %w", job.ID, err)
	}

	return failed, nil
}

// handBack returns a claimed job to pending at once, with its attempts
// unchanged. It returns ErrLeaseLost when the job no longer belongs to the
// job's claim token.
func handBack(ctx context.Context, q db.DBTX, job db.Job) error {
	rows, err := db.New(q).HandBackJob(ctx, db.HandBackJobParams{ID: job.ID, ClaimToken: claimToken(job)})
	if err != nil {
		return fmt.Errorf("hand back job %d: %w", job.ID, err)
	}

	if rows == 0 {
		return ErrLeaseLost
	}

	return nil
}

// claimToken returns the token of a claimed job. Claims always set it; a
// job that was never claimed gets 0, which matches no row.
func claimToken(job db.Job) int64 {
	if job.ClaimToken == nil {
		return 0
	}

	return *job.ClaimToken
}

// errorText returns the message of err as text Postgres accepts: valid
// UTF-8 with no NUL bytes, cut to maxErrorBytes. Cutting can split a
// character, so the check for valid UTF-8 runs after it.
func errorText(err error) string {
	if err == nil {
		return ""
	}

	text := err.Error()
	if len(text) > maxErrorBytes {
		text = text[:maxErrorBytes]
	}

	return strings.ReplaceAll(strings.ToValidUTF8(text, ""), "\x00", "")
}

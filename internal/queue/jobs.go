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

// txBeginner starts a transaction: a pool, a connection, or a transaction,
// which starts a savepoint.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// claim leases up to batch ready jobs of kind to worker for lease. It runs
// in a transaction of its own, so that a claim whose caller gives up before
// the commit is rolled back. When the commit fails, claim returns the jobs
// it leased with the error: the commit may have gone through, so the caller
// hands them back with handBackLost.
func claim(ctx context.Context, q txBeginner, worker, kind string, lease time.Duration, batch int32) ([]db.Job, error) {
	var jobs []db.Job

	err := pgx.BeginFunc(ctx, q, func(tx pgx.Tx) error {
		var err error

		jobs, err = db.New(tx).ClaimJobs(ctx, db.ClaimJobsParams{
			Worker:       worker,
			LeaseSeconds: lease.Seconds(),
			Kinds:        []string{kind},
			Batch:        batch,
		})
		if err != nil {
			return fmt.Errorf("run the claim: %w", err)
		}

		return nil
	})
	if err != nil {
		return jobs, fmt.Errorf("claim %s jobs: %w", kind, err)
	}

	return jobs, nil
}

// complete marks a claimed job done. It returns ErrLeaseLost when the job no
// longer belongs to the job's claim token.
func complete(ctx context.Context, q db.DBTX, job db.Job) error {
	rows, err := db.New(q).CompleteJob(ctx, db.CompleteJobParams{ID: job.ID, ClaimToken: ClaimToken(job)})
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
		ClaimToken:     ClaimToken(job),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Job{}, ErrLeaseLost
	}

	if err != nil {
		return db.Job{}, fmt.Errorf("fail job %d: %w", job.ID, err)
	}

	return failed, nil
}

// handBackLost hands back jobs whose claim may have committed although its
// result never reached the caller, for example when the commit's reply came
// after the caller's timeout. It first waits for the claim's transaction to
// end, so that it sees whether the claim committed. Each hand-back checks
// the job's claim token, so a job whose claim did not commit, or that another
// claim owns by now, is left alone. It returns the jobs it handed back.
func handBackLost(ctx context.Context, q db.DBTX, jobs []db.Job) ([]db.Job, error) {
	ids := make([]int64, len(jobs))
	for i, job := range jobs {
		ids[i] = job.ID
	}

	if err := db.New(q).WaitForJobWriters(ctx, ids); err != nil {
		return nil, fmt.Errorf("wait for the claim to end: %w", err)
	}

	var (
		handedBack []db.Job
		errs       []error
	)

	for _, job := range jobs {
		err := handBack(ctx, q, job)
		if errors.Is(err, ErrLeaseLost) {
			continue
		}

		if err != nil {
			errs = append(errs, err)

			continue
		}

		handedBack = append(handedBack, job)
	}

	if len(errs) > 0 {
		return handedBack, fmt.Errorf("hand back lost claims: %w", errors.Join(errs...))
	}

	return handedBack, nil
}

// handBack returns a claimed job to pending at once, with its attempts
// unchanged. It returns ErrLeaseLost when the job no longer belongs to the
// job's claim token.
func handBack(ctx context.Context, q db.DBTX, job db.Job) error {
	rows, err := db.New(q).HandBackJob(ctx, db.HandBackJobParams{ID: job.ID, ClaimToken: ClaimToken(job)})
	if err != nil {
		return fmt.Errorf("hand back job %d: %w", job.ID, err)
	}

	if rows == 0 {
		return ErrLeaseLost
	}

	return nil
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

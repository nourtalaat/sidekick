//go:build integration

package queue

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	// lostClaimWorker is the WorkerID of the Runner whose claim is slowed.
	lostClaimWorker = "lost-claim-worker"
	// lostClaimTimeout is that Runner's claim timeout.
	lostClaimTimeout = time.Second
	// slowClaimSleep is how long the slowed claim takes in the database,
	// well past lostClaimTimeout and far short of the jobs' lease.
	slowClaimSleep = 3 * time.Second
)

// TestRunnerDoesNotLeaveLostClaimsRunning slows one claim of a Runner in the
// database past the Runner's claim timeout, so that the Runner gives up on it
// while the database goes on with it, and checks that every job still runs
// once, soon, and without a counted attempt. A job left running by the lost
// claim would wait for its 5 minute lease to end, past the test's wait.
func TestRunnerDoesNotLeaveLostClaimsRunning(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind string
		// jobs is how many jobs are ready when the Runner starts, and the
		// kind's concurrency, so that one round of claims takes them all.
		jobs int
		// inCommit slows the claim's commit, and not its statement.
		inCommit bool
		// slowRow picks the claimed row whose claim is slowed, counting
		// from 1 in the order the Runner claims them.
		slowRow int
	}{
		{name: "a claim statement that ends after the timeout", kind: KindPublish, jobs: 1, slowRow: 1},
		{name: "a claim commit that ends after the timeout", kind: KindPublish, jobs: 1, inCommit: true, slowRow: 1},
		{name: "a limited claim commit that ends after the timeout", kind: KindInvestigate, jobs: 1, inCommit: true, slowRow: 1},
		{name: "a limited claim that times out after an earlier one committed", kind: KindInvestigate, jobs: 2, slowRow: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pool := setup(t)
			slowOneClaim(t, pool, tt.inCommit, tt.slowRow)

			ids := make([]int64, tt.jobs)
			for i := range ids {
				ids[i] = mustEnqueue(t, pool, NewJob{Kind: tt.kind})
			}

			runs := newRunCounter()
			runner := newTestRunner(t, pool, Options{WorkerID: lostClaimWorker, Limits: Limits{Global: tt.jobs}})
			runner.claimTimeout = lostClaimTimeout

			require.NoError(t, runner.Register(tt.kind, HandlerFunc(func(_ context.Context, job db.Job) error {
				runs.add(job.ID, lostClaimWorker)

				return nil
			}), KindOptions{Concurrency: tt.jobs}))

			stop := startRunners(t, runner)

			waitForCount(t, pool, StatusDone, tt.jobs)
			stop()

			counts, _ := runs.snapshot()
			for _, id := range ids {
				require.Equal(t, 1, counts[id], "job %d", id)
				require.Zero(t, getJob(t, pool, id).Attempts, "job %d was handed back, not reaped", id)
			}
		})
	}
}

// slowOneClaim makes one claim of lostClaimWorker sleep for slowClaimSleep in
// the database: the claim of the slowRow-th row it claims, in the claim's
// statement, or in its commit when inCommit is set. A sequence counts the
// rows, since it is not rolled back with a claim that fails.
func slowOneClaim(t *testing.T, pool *pgxpool.Pool, inCommit bool, slowRow int) {
	t.Helper()

	trigger := "CREATE TRIGGER slow_claim BEFORE UPDATE ON jobs FOR EACH ROW EXECUTE FUNCTION slow_claim();"
	if inCommit {
		trigger = "CREATE CONSTRAINT TRIGGER slow_claim AFTER UPDATE ON jobs DEFERRABLE INITIALLY DEFERRED " +
			"FOR EACH ROW EXECUTE FUNCTION slow_claim();"
	}

	_, err := pool.Exec(t.Context(), fmt.Sprintf(`
CREATE SEQUENCE slow_claim_rows;

CREATE FUNCTION slow_claim() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.status = 'pending' AND NEW.status = 'running' AND NEW.locked_by = '%s' THEN
    IF nextval('slow_claim_rows') = %d THEN
      PERFORM pg_sleep(%f);
    END IF;
  END IF;

  RETURN NEW;
END
$$;

%s`, lostClaimWorker, slowRow, slowClaimSleep.Seconds(), trigger))
	require.NoError(t, err)
}

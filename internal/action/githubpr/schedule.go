package githubpr

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// uniqueKeyPrefix starts the unique key of every status job, which is
// followed by its date.
const uniqueKeyPrefix = "pr_status:"

// scheduleLockKey is the transaction-level advisory lock that makes the
// check for today's job and its enqueue one step across replicas.
const scheduleLockKey int64 = 0x736b2d7072737461

// SchedulerOptions set up the scheduler of the status job. Pool is required.
type SchedulerOptions struct {
	Pool *pgxpool.Pool
	// Interval is how often the scheduler checks. Zero means
	// queue.DefaultDailyInterval.
	Interval time.Duration
	// Logger gets a line when a check fails. Nil logs nothing.
	Logger *slog.Logger
	// Now is the clock whose UTC date names the day. Nil means time.Now.
	Now func() time.Time
}

// NewScheduler returns the scheduler that enqueues the status job of each
// day, with the unique key pr_status:<date>. Run one in every replica:
// however many there are, the job of a day runs once.
func NewScheduler(opts SchedulerOptions) *queue.DailyScheduler {
	return queue.NewDailyScheduler(queue.DailyOptions{
		Pool:      opts.Pool,
		Kind:      queue.KindCheckPullRequests,
		KeyPrefix: uniqueKeyPrefix,
		LockKey:   scheduleLockKey,
		Exists:    jobExists,
		Interval:  opts.Interval,
		Logger:    opts.Logger,
		Now:       opts.Now,
	})
}

// jobExists reports whether a status job with the unique key exists in any
// status. Its query names the kind as a literal, so the partial index on
// jobs for that kind applies.
func jobExists(ctx context.Context, tx pgx.Tx, key string) (bool, error) {
	found, err := db.New(tx).PullRequestJobExists(ctx, key)
	if err != nil {
		return false, fmt.Errorf("look for the status job %s: %w", key, err)
	}

	return found, nil
}

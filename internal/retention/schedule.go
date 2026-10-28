package retention

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultScheduleInterval is how often a Scheduler checks that the job of
// the day is enqueued.
const DefaultScheduleInterval = time.Hour

// uniqueKeyPrefix starts the unique key of every retention job, which is
// followed by its date.
const uniqueKeyPrefix = "retention:"

// scheduleLockKey is the transaction-level advisory lock that makes the
// check for today's job and its enqueue one step across replicas.
const scheduleLockKey int64 = 0x736b2d7265746e74

// SchedulerOptions set up a Scheduler. Pool is required.
type SchedulerOptions struct {
	Pool *pgxpool.Pool
	// Interval is how often the Scheduler checks. Zero means
	// DefaultScheduleInterval.
	Interval time.Duration
	// Logger gets a line when a check fails. Nil logs nothing.
	Logger *slog.Logger
	// Now is the clock whose UTC date names the day. Nil means time.Now.
	Now func() time.Time
}

// Scheduler enqueues the retention job of each day. Run one in every
// replica: however many there are, the job of a day runs once.
type Scheduler struct {
	pool     *pgxpool.Pool
	interval time.Duration
	log      *slog.Logger
	now      func() time.Time
}

// payload is the payload of a retention job.
type payload struct {
	// Day is the UTC date the job is for, such as 2026-10-28.
	Day string `json:"day"`
}

// NewScheduler returns a Scheduler.
func NewScheduler(opts SchedulerOptions) *Scheduler {
	s := &Scheduler{pool: opts.Pool, interval: opts.Interval, log: opts.Logger, now: opts.Now}

	if s.interval <= 0 {
		s.interval = DefaultScheduleInterval
	}

	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}

	if s.now == nil {
		s.now = time.Now
	}

	return s
}

// Run schedules the job of the day at once and then every interval, until
// ctx ends. A failed check is logged and tried again at the next one, for
// example while the schedule waits for a manual migration.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		if _, err := s.Schedule(ctx); err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "scheduling the retention job failed; trying again later", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Schedule enqueues the retention job of today, as a UTC date, unless a
// job for today exists already in any status, and reports whether it
// enqueued one. A job that is done or dead counts, so a replica that
// starts later in the day does not run the job again.
func (s *Scheduler) Schedule(ctx context.Context) (bool, error) {
	day := s.now().UTC().Format(time.DateOnly)
	key := uniqueKeyPrefix + day

	body, err := json.Marshal(payload{Day: day})
	if err != nil {
		return false, fmt.Errorf("encode the payload: %w", err)
	}

	var enqueued bool

	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		if err := queries.LockRetentionSchedule(ctx, scheduleLockKey); err != nil {
			return fmt.Errorf("lock the schedule: %w", err)
		}

		found, err := queries.RetentionJobExists(ctx, key)
		if err != nil {
			return fmt.Errorf("look for the job: %w", err)
		}

		if found {
			return nil
		}

		_, err = queue.Enqueue(ctx, tx, queue.NewJob{Kind: queue.KindRetention, Payload: body, UniqueKey: key})
		if errors.Is(err, queue.ErrDuplicate) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("enqueue: %w", err)
		}

		enqueued = true

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("schedule the retention job of %s: %w", day, err)
	}

	return enqueued, nil
}

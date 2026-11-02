package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultDailyInterval is how often a DailyScheduler checks that the job of
// the day is enqueued.
const DefaultDailyInterval = time.Hour

// DailyOptions set up a DailyScheduler. Pool, Kind, KeyPrefix, LockKey and
// Exists are required.
type DailyOptions struct {
	Pool *pgxpool.Pool
	// Kind is the kind of the job to enqueue.
	Kind string
	// KeyPrefix starts the unique key of each day's job, which is the prefix
	// and the UTC date, for example "retention:2026-10-28".
	KeyPrefix string
	// LockKey is the advisory lock that makes the check for today's job and
	// its enqueue one step across replicas. Give each kind of job its own.
	LockKey int64
	// Exists reports whether a job of the kind with the unique key exists in
	// any status: pending, running, done or dead. It runs in the transaction
	// that holds the lock. Write it with the kind as a literal, so the
	// kind's partial index on jobs applies.
	Exists func(ctx context.Context, tx pgx.Tx, key string) (bool, error)
	// Interval is how often the scheduler checks. Zero means
	// DefaultDailyInterval.
	Interval time.Duration
	// Logger gets a line when a check fails. Nil logs nothing.
	Logger *slog.Logger
	// Now is the clock whose UTC date names the day. Nil means time.Now.
	Now func() time.Time
}

// DailyScheduler enqueues one job of a kind each day. Run one in every
// replica: however many there are, the job of a day is enqueued once.
type DailyScheduler struct {
	opts DailyOptions
	log  *slog.Logger
	now  func() time.Time
}

// dailyPayload is the payload of a daily job.
type dailyPayload struct {
	// Day is the UTC date the job is for, such as 2026-10-28.
	Day string `json:"day"`
}

// NewDailyScheduler returns a DailyScheduler.
func NewDailyScheduler(opts DailyOptions) *DailyScheduler {
	s := &DailyScheduler{opts: opts, log: opts.Logger, now: opts.Now}

	if s.opts.Interval <= 0 {
		s.opts.Interval = DefaultDailyInterval
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
func (s *DailyScheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()

	for {
		if _, err := s.Schedule(ctx); err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "scheduling the daily job failed; trying again later", "kind", s.opts.Kind, "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Schedule enqueues the job of today, as a UTC date, unless a job for today
// exists already in any status, and reports whether it enqueued one. A job
// that is done or dead counts, so a replica that starts later in the day
// does not run the job again.
func (s *DailyScheduler) Schedule(ctx context.Context) (bool, error) {
	day := s.now().UTC().Format(time.DateOnly)
	key := s.opts.KeyPrefix + day

	body, err := json.Marshal(dailyPayload{Day: day})
	if err != nil {
		return false, fmt.Errorf("encode the payload: %w", err)
	}

	var enqueued bool

	err = pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		if err := db.New(tx).LockJobSchedule(ctx, s.opts.LockKey); err != nil {
			return fmt.Errorf("lock the schedule: %w", err)
		}

		found, err := s.opts.Exists(ctx, tx, key)
		if err != nil {
			return fmt.Errorf("look for the job: %w", err)
		}

		if found {
			return nil
		}

		_, err = Enqueue(ctx, tx, NewJob{Kind: s.opts.Kind, Payload: body, UniqueKey: key})
		if errors.Is(err, ErrDuplicate) {
			return nil
		}

		if err != nil {
			return fmt.Errorf("enqueue: %w", err)
		}

		enqueued = true

		return nil
	})
	if err != nil {
		return false, fmt.Errorf("schedule the %s job of %s: %w", s.opts.Kind, day, err)
	}

	return enqueued, nil
}

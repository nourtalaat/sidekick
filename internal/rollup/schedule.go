package rollup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/queue"
)

// DefaultScheduleInterval is how often a Scheduler enqueues the job.
const DefaultScheduleInterval = time.Minute

// UniqueKey is the unique key of the rollup job. While one is pending or
// running, no replica adds another.
const UniqueKey = "rollup"

// SchedulerOptions set up a Scheduler. Pool is required.
type SchedulerOptions struct {
	Pool *pgxpool.Pool
	// Interval is how often the Scheduler enqueues the job. Zero means
	// DefaultScheduleInterval.
	Interval time.Duration
	// Logger gets a line when an enqueue fails. Nil logs nothing.
	Logger *slog.Logger
}

// Scheduler enqueues the rollup job every interval. Run one in every
// replica.
type Scheduler struct {
	pool     *pgxpool.Pool
	interval time.Duration
	log      *slog.Logger
}

// NewScheduler returns a Scheduler.
func NewScheduler(opts SchedulerOptions) *Scheduler {
	s := &Scheduler{pool: opts.Pool, interval: opts.Interval, log: opts.Logger}

	if s.interval <= 0 {
		s.interval = DefaultScheduleInterval
	}

	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}

	return s
}

// Run enqueues the job at once and then every interval, until ctx ends. A
// failed enqueue is logged and tried again at the next tick.
func (s *Scheduler) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		if _, err := s.Schedule(ctx); err != nil && ctx.Err() == nil {
			s.log.WarnContext(ctx, "scheduling the rollup job failed; trying again later", "error", err)
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

// Schedule enqueues the rollup job, and reports whether it enqueued one.
// It adds nothing when a job is pending or running.
func (s *Scheduler) Schedule(ctx context.Context) (bool, error) {
	_, err := queue.Enqueue(ctx, s.pool, queue.NewJob{Kind: queue.KindRollup, Payload: json.RawMessage(`{}`), UniqueKey: UniqueKey})
	if errors.Is(err, queue.ErrDuplicate) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("enqueue the rollup job: %w", err)
	}

	return true, nil
}

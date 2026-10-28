// Package backfill fills columns that a migration added to rows that
// existed before it, with a one-time job that works in small batches.
//
// A migration that rewrote every row would hold locks on a table of a
// live install for as long as it took. Instead the migration only adds
// the nullable columns, a Scheduler enqueues one job when it finds rows
// still unfilled, and the Handler fills them in batches, each in a
// transaction of its own. Until the job finishes, the unfilled rows do
// not match the filters on the new columns.
package backfill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultBatchSize is the most rows one batch fills.
const DefaultBatchSize = 5000

// DefaultRetryInterval is how long a Scheduler waits before it checks
// again after a failed check, for example while it waits for a manual
// migration.
const DefaultRetryInterval = time.Minute

// InvestigationListKey is the unique key of the job that fills the
// investigation list's columns. A job that is pending or running holds it.
const InvestigationListKey = "backfill:investigation_list"

// Options set up a Handler. Pool is required.
type Options struct {
	Pool *pgxpool.Pool
	// BatchSize is the most rows one batch fills. Zero means
	// DefaultBatchSize.
	BatchSize int32
	// Logger gets a line when the job ends. Nil logs nothing.
	Logger *slog.Logger
}

// Handler runs the backfill job of the investigation list. Make it with
// New and register it for queue.KindBackfill.
type Handler struct {
	pool  *pgxpool.Pool
	batch int32
	log   *slog.Logger
}

// New returns a Handler.
func New(opts Options) *Handler {
	h := &Handler{pool: opts.Pool, batch: opts.BatchSize, log: opts.Logger}

	if h.batch <= 0 {
		h.batch = DefaultBatchSize
	}

	if h.log == nil {
		h.log = slog.New(slog.DiscardHandler)
	}

	return h
}

// Handle fills the list's columns of every investigation that lacks them,
// oldest first, one batch at a time. A batch can fill fewer rows than its
// size, because a row updated while the batch ran is left for the next one,
// so the job stops only when a batch fills no row. A run that stops part
// way leaves only fewer rows behind, so running the job again is safe.
func (h *Handler) Handle(ctx context.Context, job db.Job) error {
	queries := db.New(h.pool)

	var total int64

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("stop: %w", err)
		}

		n, err := queries.BackfillInvestigationList(ctx, h.batch)
		if err != nil {
			return fmt.Errorf("fill a batch of investigations: %w", err)
		}

		total += n

		if n == 0 {
			break
		}
	}

	h.log.InfoContext(ctx, "filled the investigation list columns", "job_id", job.ID, "investigations", total)

	return nil
}

// SchedulerOptions set up a Scheduler. Pool is required.
type SchedulerOptions struct {
	Pool *pgxpool.Pool
	// RetryInterval is the wait before a failed check is tried again.
	// Zero means DefaultRetryInterval.
	RetryInterval time.Duration
	// Logger gets a line when a check fails. Nil logs nothing.
	Logger *slog.Logger
}

// Scheduler enqueues the backfill job when rows still need it. Run one in
// every replica: the unique key lets one job run at a time.
type Scheduler struct {
	pool  *pgxpool.Pool
	retry time.Duration
	log   *slog.Logger
}

// NewScheduler returns a Scheduler.
func NewScheduler(opts SchedulerOptions) *Scheduler {
	s := &Scheduler{pool: opts.Pool, retry: opts.RetryInterval, log: opts.Logger}

	if s.retry <= 0 {
		s.retry = DefaultRetryInterval
	}

	if s.log == nil {
		s.log = slog.New(slog.DiscardHandler)
	}

	return s
}

// Run enqueues the job at once when rows need it and then returns nil, or
// tries again after the retry interval when the check fails, until ctx
// ends, which it reports as an error, as the app ignores at shutdown. A
// new row never needs the job, so there is nothing to check later.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		_, err := s.Schedule(ctx)
		if err == nil {
			return nil
		}

		s.log.WarnContext(ctx, "scheduling the investigation list backfill failed; trying again later", "error", err)

		select {
		case <-ctx.Done():
			return fmt.Errorf("stop: %w", context.Cause(ctx))
		case <-time.After(s.retry):
		}
	}
}

// Schedule enqueues the job when an investigation still lacks the list's
// columns, and reports whether it enqueued one. It adds nothing when a
// job is pending or running.
func (s *Scheduler) Schedule(ctx context.Context) (bool, error) {
	unlisted, err := db.New(s.pool).AnyUnlistedInvestigation(ctx)
	if err != nil {
		return false, fmt.Errorf("look for investigations to fill: %w", err)
	}

	if !unlisted {
		return false, nil
	}

	_, err = queue.Enqueue(ctx, s.pool, queue.NewJob{Kind: queue.KindBackfill, Payload: json.RawMessage(`{}`), UniqueKey: InvestigationListKey})
	if errors.Is(err, queue.ErrDuplicate) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("enqueue the investigation list backfill: %w", err)
	}

	return true, nil
}

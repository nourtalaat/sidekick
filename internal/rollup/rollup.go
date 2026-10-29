// Package rollup keeps the dashboard's tables, stats_hourly and
// quality_daily, in step with the investigations they count.
//
// Triggers mark the hours and days that a change touches in rollup_dirty
// (see migration 00039). A Scheduler in every replica enqueues a rollup job
// once a minute, with a unique key so that one runs at a time across
// replicas, and a Handler runs it. The handler takes the dirty buckets of
// each table in batches, deletes the table's rows of those buckets and
// counts them again from the raw tables, all in one transaction, so a
// failed run puts the marks back and a run that is repeated gives the same
// rows.
package rollup

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultBatchSize is the most buckets one transaction takes and counts
// again.
const DefaultBatchSize = 1000

// The tables the job keeps up to date, as rollup_dirty and the metrics name
// them.
const (
	TableStatsHourly  = "stats_hourly"
	TableQualityDaily = "quality_daily"
)

// Options set up a Handler. Pool is required.
type Options struct {
	Pool *pgxpool.Pool
	// BatchSize is the most buckets one transaction takes. Zero means
	// DefaultBatchSize.
	BatchSize int32
	// Metrics count the buckets and time the runs. Nil records nothing.
	Metrics *Metrics
	// Logger gets a line per run that counted buckets, with how long it
	// took. Nil logs nothing.
	Logger *slog.Logger
}

// Handler runs rollup jobs. Make it with New and register it for
// queue.KindRollup.
type Handler struct {
	pool    *pgxpool.Pool
	batch   int32
	metrics *Metrics
	log     *slog.Logger
	tables  []table
}

// table is one rollup table: its name, and how to count a batch of its
// buckets again. recount deletes the rows of the buckets and adds them
// back.
type table struct {
	name    string
	recount func(ctx context.Context, queries *db.Queries, buckets []time.Time) error
}

// New returns a Handler.
func New(opts Options) *Handler {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	h := &Handler{pool: opts.Pool, batch: opts.BatchSize, metrics: opts.Metrics, log: log}

	if h.batch <= 0 {
		h.batch = DefaultBatchSize
	}

	h.tables = []table{
		{name: TableStatsHourly, recount: recountStatsHourly},
		{name: TableQualityDaily, recount: recountQualityDaily},
	}

	return h
}

// Handle counts the dirty buckets of each table again, a batch at a time,
// until none is left that no writer holds a lock on. It logs how many
// buckets it counted and how long it took.
func (h *Handler) Handle(ctx context.Context, job db.Job) error {
	start := time.Now()
	args := []any{"job_id", job.ID}

	var total int64

	for _, t := range h.tables {
		n, err := h.recountAll(ctx, t)
		if err != nil {
			return fmt.Errorf("recount %s: %w", t.name, err)
		}

		total += n

		args = append(args, t.name+"_buckets", n)
	}

	elapsed := time.Since(start)
	h.metrics.ran(elapsed)

	waiting, err := db.New(h.pool).CountDirtyBuckets(ctx)
	if err != nil {
		return fmt.Errorf("count the buckets that wait: %w", err)
	}

	h.metrics.waiting(waiting)

	if total > 0 {
		h.log.InfoContext(ctx, "recounted dirty buckets", append(args, "duration_ms", elapsed.Milliseconds(), "still_dirty", waiting)...)
	}

	return nil
}

// recountAll counts the dirty buckets of t again, one batch at a time,
// until a batch takes fewer buckets than the batch size. It returns how
// many buckets it counted.
func (h *Handler) recountAll(ctx context.Context, t table) (int64, error) {
	var total int64

	for {
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("stop: %w", err)
		}

		n, err := h.recountBatch(ctx, t)
		if err != nil {
			return total, fmt.Errorf("recount a batch: %w", err)
		}

		total += n

		if n < int64(h.batch) {
			return total, nil
		}
	}
}

// recountBatch takes a batch of the dirty buckets of t and counts them
// again in one transaction, and returns how many it took. The take comes
// first, at READ COMMITTED, so the recount sees every change whose mark the
// take saw. A mark that commits after the take is not deleted, and the next
// run counts it.
func (h *Handler) recountBatch(ctx context.Context, t table) (int64, error) {
	var n int64

	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		buckets, err := queries.TakeDirtyBuckets(ctx, db.TakeDirtyBucketsParams{TableName: t.name, Batch: h.batch})
		if err != nil {
			return fmt.Errorf("take the dirty buckets: %w", err)
		}

		if len(buckets) == 0 {
			return nil
		}

		if err := t.recount(ctx, queries, buckets); err != nil {
			return fmt.Errorf("count %d buckets: %w", len(buckets), err)
		}

		n = int64(len(buckets))

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("recount %s: %w", t.name, err)
	}

	h.metrics.counted(t.name, n)

	return n, nil
}

func recountStatsHourly(ctx context.Context, queries *db.Queries, buckets []time.Time) error {
	if err := queries.DeleteStatsHourly(ctx, buckets); err != nil {
		return fmt.Errorf("delete the old rows: %w", err)
	}

	if err := queries.RecountStatsHourly(ctx, buckets); err != nil {
		return fmt.Errorf("count: %w", err)
	}

	return nil
}

func recountQualityDaily(ctx context.Context, queries *db.Queries, buckets []time.Time) error {
	if err := queries.DeleteQualityDaily(ctx, buckets); err != nil {
		return fmt.Errorf("delete the old rows: %w", err)
	}

	if err := queries.RecountQualityDaily(ctx, buckets); err != nil {
		return fmt.Errorf("count: %w", err)
	}

	return nil
}

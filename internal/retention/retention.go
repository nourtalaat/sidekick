// Package retention deletes old records once a day, as the retention
// settings say.
//
// A Scheduler in every replica enqueues one retention job per day, with
// the unique key retention:<date>, and a Handler runs it. The handler
// deletes each kind of record in batches, oldest first, each batch in a
// short transaction of its own, so it never holds locks for long.
package retention

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/store/db"
)

// DefaultBatchSize is the most rows one batch deletes.
const DefaultBatchSize = 5000

// How long finished jobs are kept. They are not settings.
const (
	DoneJobsPeriod = 24 * time.Hour
	DeadJobsPeriod = 30 * 24 * time.Hour
)

// The tables the metrics name. Rows that a foreign key deletes with their
// investigation, such as its steps and inbox rows, are not counted.
const (
	TableInvestigations = "investigations"
	TableWatchers       = "watchers"
	TableEventGroups    = "event_groups"
	TableSteps          = "steps"
	TableWatcherChecks  = "watcher_checks"
	TableEvents         = "events"
	TableAuditLog       = "audit_log"
	TableJobs           = "jobs"
)

// Periods are how long each kind of record is kept, from the retention
// section of the config. Each must be positive.
type Periods struct {
	Events         time.Duration
	Investigations time.Duration
	Steps          time.Duration
	AuditLog       time.Duration
	WatcherChecks  time.Duration
}

// Options set up a Handler. Pool is required.
type Options struct {
	Pool    *pgxpool.Pool
	Periods Periods
	// BatchSize is the most rows one batch deletes. Zero means
	// DefaultBatchSize.
	BatchSize int32
	// Metrics count the deleted rows. Nil counts nothing.
	Metrics *Metrics
	// Logger gets a line per run with the rows deleted. Nil logs nothing.
	Logger *slog.Logger
	// Now is the clock the periods are measured from. Nil means time.Now.
	Now func() time.Time
}

// Handler runs retention jobs. Make it with New and register it for
// queue.KindRetention as a long job.
type Handler struct {
	pool    *pgxpool.Pool
	batch   int32
	metrics *Metrics
	log     *slog.Logger
	now     func() time.Time
	tables  []table
}

// table is one kind of record the job deletes: its name in the metrics
// and the logs, how long it is kept, and how to delete one batch of rows
// older than before. A batch returns how many rows it took; fewer than
// the batch size means none are left.
type table struct {
	name   string
	period time.Duration
	delete func(ctx context.Context, before time.Time, batch int32) (int64, error)
}

// New returns a Handler.
func New(opts Options) *Handler {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	h := &Handler{
		pool:    opts.Pool,
		batch:   opts.BatchSize,
		metrics: opts.Metrics,
		log:     log,
		now:     opts.Now,
	}

	if h.batch <= 0 {
		h.batch = DefaultBatchSize
	}

	if h.now == nil {
		h.now = time.Now
	}

	p := opts.Periods
	// Investigations go first: their steps, inbox rows and watchers go
	// with them, so the later tables have fewer rows left to delete.
	// TODO: [SK-40] add memory_entries here with retention.memory.
	h.tables = []table{
		{name: TableInvestigations, period: p.Investigations, delete: h.deleteInvestigations},
		{name: TableSteps, period: p.Steps, delete: h.deleteSteps},
		{name: TableWatcherChecks, period: p.WatcherChecks, delete: h.deleteWatcherChecks},
		{name: TableEvents, period: p.Events, delete: h.deleteEvents},
		{name: TableAuditLog, period: p.AuditLog, delete: h.deleteAuditEntries},
		{name: TableJobs, period: DoneJobsPeriod, delete: h.deleteDoneJobs},
		{name: TableJobs, period: DeadJobsPeriod, delete: h.deleteDeadJobs},
	}

	return h
}

// Handle deletes every record older than its period, table by table. A
// run that stops part way leaves only fewer rows behind, so running the
// job again is safe.
func (h *Handler) Handle(ctx context.Context, job db.Job) error {
	now := h.now()
	deleted := map[string]int64{}

	for _, t := range h.tables {
		if t.period <= 0 {
			continue
		}

		n, err := h.deleteAll(ctx, t, now.Add(-t.period))
		deleted[t.name] += n

		if err != nil {
			return fmt.Errorf("delete old %s: %w", t.name, err)
		}
	}

	args := []any{"job_id", job.ID}
	for _, t := range h.tables {
		if n, ok := deleted[t.name]; ok {
			args = append(args, t.name, n)
			delete(deleted, t.name)
		}
	}

	h.log.InfoContext(ctx, "deleted old records", args...)

	return nil
}

// deleteAll deletes the rows of t older than before, one batch at a time,
// until a batch takes fewer rows than the batch size. It returns how many
// rows it deleted.
func (h *Handler) deleteAll(ctx context.Context, t table, before time.Time) (int64, error) {
	var total int64

	for {
		if err := ctx.Err(); err != nil {
			return total, fmt.Errorf("stop: %w", err)
		}

		n, err := t.delete(ctx, before, h.batch)
		if err != nil {
			return total, fmt.Errorf("delete a batch: %w", err)
		}

		total += n

		if n < int64(h.batch) {
			return total, nil
		}
	}
}

// deleteInvestigations deletes one batch of old investigations in one
// transaction, with what belongs to them: their steps, inbox rows, output
// refs, publish states and feedback through the foreign keys, and their
// watchers and event groups here. It returns how many investigations it
// took.
func (h *Handler) deleteInvestigations(ctx context.Context, before time.Time, batch int32) (int64, error) {
	var locked, watchers, groups int64

	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		rows, err := queries.LockOldInvestigations(ctx, db.LockOldInvestigationsParams{Before: before, Batch: batch})
		if err != nil {
			return fmt.Errorf("lock the investigations: %w", err)
		}

		locked = int64(len(rows))
		if locked == 0 {
			return nil
		}

		ids, groupIDs := splitIDs(rows)

		if watchers, err = queries.DeleteWatchersOf(ctx, db.DeleteWatchersOfParams{InvestigationIds: ids, GroupIds: groupIDs}); err != nil {
			return fmt.Errorf("delete their watchers: %w", err)
		}

		if _, err := queries.DeleteInvestigations(ctx, ids); err != nil {
			return fmt.Errorf("delete them: %w", err)
		}

		if groups, err = queries.DeleteUnusedEventGroups(ctx, groupIDs); err != nil {
			return fmt.Errorf("delete their event groups: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete investigations: %w", err)
	}

	h.metrics.deleted(TableInvestigations, locked)
	h.metrics.deleted(TableWatchers, watchers)
	h.metrics.deleted(TableEventGroups, groups)

	return locked, nil
}

// deleteAuditEntries deletes one batch of old audit log entries, in a
// transaction that allows it.
func (h *Handler) deleteAuditEntries(ctx context.Context, before time.Time, batch int32) (int64, error) {
	var n int64

	err := pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		if err := queries.AllowAuditLogDelete(ctx); err != nil {
			return fmt.Errorf("allow the delete: %w", err)
		}

		var err error
		if n, err = queries.DeleteOldAuditEntries(ctx, db.DeleteOldAuditEntriesParams{Before: before, Batch: batch}); err != nil {
			return fmt.Errorf("delete: %w", err)
		}

		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("delete audit log entries: %w", err)
	}

	h.metrics.deleted(TableAuditLog, n)

	return n, nil
}

func (h *Handler) deleteSteps(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return h.counted(TableSteps)(db.New(h.pool).DeleteOldSteps(ctx, db.DeleteOldStepsParams{Before: before, Batch: batch}))
}

func (h *Handler) deleteWatcherChecks(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return h.counted(TableWatcherChecks)(db.New(h.pool).DeleteOldWatcherChecks(ctx, db.DeleteOldWatcherChecksParams{Before: before, Batch: batch}))
}

func (h *Handler) deleteEvents(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return h.counted(TableEvents)(db.New(h.pool).DeleteOldEvents(ctx, db.DeleteOldEventsParams{Before: before, Batch: batch}))
}

func (h *Handler) deleteDoneJobs(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return h.counted(TableJobs)(db.New(h.pool).DeleteDoneJobs(ctx, db.DeleteDoneJobsParams{Before: before, Batch: batch}))
}

func (h *Handler) deleteDeadJobs(ctx context.Context, before time.Time, batch int32) (int64, error) {
	return h.counted(TableJobs)(db.New(h.pool).DeleteDeadJobs(ctx, db.DeleteDeadJobsParams{Before: before, Batch: batch}))
}

// counted returns a function that counts the rows a one-statement delete
// of name deleted, and wraps its error.
func (h *Handler) counted(name string) func(n int64, err error) (int64, error) {
	return func(n int64, err error) (int64, error) {
		if err != nil {
			return 0, fmt.Errorf("delete %s: %w", name, err)
		}

		h.metrics.deleted(name, n)

		return n, nil
	}
}

// splitIDs returns the investigation IDs of rows and their distinct group
// IDs.
func splitIDs(rows []db.LockOldInvestigationsRow) (ids, groupIDs []uuid.UUID) {
	seen := make(map[uuid.UUID]bool, len(rows))

	for _, r := range rows {
		ids = append(ids, r.ID)

		if !seen[r.GroupID] {
			seen[r.GroupID] = true
			groupIDs = append(groupIDs, r.GroupID)
		}
	}

	return ids, groupIDs
}

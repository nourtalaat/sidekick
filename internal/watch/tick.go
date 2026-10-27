package watch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// errMissingTickerOption is returned by NewTicker without a required
// option.
var errMissingTickerOption = errors.New("new watch ticker: Pool, Checker and CloseGroup are required")

// TickerOptions set up a Ticker. Pool, Checker and CloseGroup are
// required.
type TickerOptions struct {
	// Pool is the database the watchers, groups and jobs are in.
	Pool *pgxpool.Pool
	// Checker runs the checks.
	Checker *Checker
	// Policy is what the config allows. A watcher whose check it no longer
	// allows stops with config_changed.
	Policy Policy
	// CloseGroup enqueues the close_group job of a group in q, which a
	// watcher that reached a final state calls so a group kept open for
	// its watchers can close.
	CloseGroup func(ctx context.Context, q db.DBTX, groupID uuid.UUID) error
	// Logger receives a line per state change, with IDs and names only,
	// never check details. Nil logs nothing.
	Logger *slog.Logger
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Ticker runs watch_tick jobs. Make it with NewTicker and register it for
// queue.KindWatchTick. It holds no state between jobs, so every replica
// can run any watcher's ticks.
type Ticker struct {
	opts TickerOptions
	log  *slog.Logger
	now  func() time.Time
}

var _ queue.Handler = (*Ticker)(nil)

// NewTicker returns a Ticker.
func NewTicker(opts TickerOptions) (*Ticker, error) {
	if opts.Pool == nil || opts.Checker == nil || opts.CloseGroup == nil {
		return nil, errMissingTickerOption
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Ticker{opts: opts, log: log, now: now}, nil
}

// Handle runs one watch_tick job: one check of its watcher. A watcher that
// is no longer running is left as it is. One past its deadline times out,
// and one whose check the config no longer allows stops. Otherwise the
// check runs, and in one transaction its result is stored, the counters
// and status are updated and, while the watcher still runs, the next tick
// is enqueued for one interval later. A watcher that reached a final
// state enqueues close_group for its group.
//
// Each tick releases the job's unique key in the transaction that
// enqueues the next one, which also fences the writes: a worker that lost
// the job's lease writes nothing. A crash repeats at most one check, and
// after an outage the next check runs once, without making up the missed
// ones. A payload that does not decode gives up the job.
func (t *Ticker) Handle(ctx context.Context, job db.Job) error {
	ctx = queue.TraceContext(ctx, job)

	id, err := decodeTickPayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the watch_tick payload: %w: %w", queue.ErrGiveUp, err)
	}

	row, err := db.New(t.opts.Pool).GetWatcherForTick(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		t.log.DebugContext(ctx, "watch_tick for a watcher that no longer exists", "watcher_id", id.String())

		return nil
	}

	if err != nil {
		return fmt.Errorf("load watcher %s: %w", id, err)
	}

	w := row.Watcher
	if w.Status != StatusRunning {
		return nil
	}

	if !t.now().Before(w.Deadline) {
		return t.end(ctx, job, w, StatusTimedOut, "")
	}

	check, reason := t.allowed(row.Route, w)
	if reason != "" {
		t.log.WarnContext(ctx, "watcher stopped: the config no longer allows its check", "watcher_id", w.ID.String(), "reason", reason)

		return t.end(ctx, job, w, StatusStopped, StopConfigChanged)
	}

	started := t.now()
	out := t.run(ctx, w, check)

	if ctx.Err() != nil {
		return fmt.Errorf("check watcher %s: %w", w.ID, context.Cause(ctx))
	}

	if err := t.record(ctx, job, w, started, out); err != nil {
		return fmt.Errorf("record the check of watcher %s: %w", w.ID, err)
	}

	return nil
}

// allowed returns the watcher's check, or why the config no longer allows
// it: the check is not valid in this deployment, the route is gone, a
// route or strategy watcher is no longer defined, or the route's limits no
// longer take a watcher the model started.
func (t *Ticker) allowed(route string, w db.Watcher) (Check, string) {
	check, err := decodeSpec(w)
	if err != nil {
		return nil, err.Error()
	}

	if err := t.opts.Checker.Validate(check); err != nil {
		return nil, err.Error()
	}

	r, ok := t.opts.Policy.Route(route)
	if !ok {
		return nil, fmt.Sprintf("route %q is not configured", route)
	}

	switch Origin(w.Origin) {
	case OriginModel:
		if !r.AllowModel {
			return nil, "the route no longer allows start_watcher"
		}

		if !slices.Contains(r.Types, check.Type()) {
			return nil, fmt.Sprintf("the route no longer allows %s checks", check.Type())
		}

		if problem := r.hostProblem(check); problem != "" {
			return nil, problem
		}
	case OriginRoute, OriginStrategy:
		if !slices.ContainsFunc(r.Definitions, func(d Definition) bool {
			return d.Name == w.Name && d.Origin == Origin(w.Origin) && d.Check.Type() == check.Type()
		}) {
			return nil, fmt.Sprintf("the %s no longer defines watcher %q", w.Origin, w.Name)
		}
	}

	return check, ""
}

// run runs the check within the watcher's check timeout.
func (t *Ticker) run(ctx context.Context, w db.Watcher, check Check) Outcome {
	timeout := time.Duration(w.CheckTimeoutS) * time.Second

	if h, ok := check.(HTTPCheck); ok && h.Timeout == 0 {
		h.Timeout = timeout
		check = h
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	target := Target{GroupID: w.GroupID}
	if w.InvestigationID != nil {
		target.InvestigationID = w.InvestigationID.String()
	}

	return t.opts.Checker.Run(ctx, target, check)
}

// record stores the outcome of a check that started at started, and ends
// the watcher or schedules its next tick, in one transaction.
func (t *Ticker) record(ctx context.Context, job db.Job, w db.Watcher, started time.Time, out Outcome) error {
	err := t.locked(ctx, job, w, func(tx pgx.Tx) error {
		queries := db.New(tx)

		counted, err := queries.RecordWatcherCheck(ctx, db.RecordWatcherCheckParams{At: started, Passed: out.Result == Pass, ID: w.ID})
		if err != nil {
			return fmt.Errorf("count the check: %w", err)
		}

		if err := queries.InsertWatcherCheck(ctx, db.InsertWatcherCheckParams{
			WatcherID:  w.ID,
			Seq:        counted.Checks,
			At:         started,
			Result:     string(out.Result),
			Detail:     optional(store.Text(out.Detail)),
			DurationMs: int32(min(out.Duration.Milliseconds(), math.MaxInt32)), //nolint:gosec // Capped at MaxInt32.
		}); err != nil {
			return fmt.Errorf("store check %d: %w", counted.Checks, err)
		}

		now := t.now()

		switch {
		case counted.PassesInARow >= counted.PassesToRecover:
			return t.endIn(ctx, tx, counted, StatusRecovered, "")
		case !now.Before(counted.Deadline):
			return t.endIn(ctx, tx, counted, StatusTimedOut, "")
		default:
			return enqueueTick(ctx, tx, w.ID, now.Add(time.Duration(counted.IntervalS)*time.Second))
		}
	})
	if err != nil {
		return fmt.Errorf("record: %w", err)
	}

	return nil
}

// end sets the watcher to a final status without a check, in its own
// transaction.
func (t *Ticker) end(ctx context.Context, job db.Job, w db.Watcher, status, stopReason string) error {
	err := t.locked(ctx, job, w, func(tx pgx.Tx) error {
		return t.endIn(ctx, tx, w, status, stopReason)
	})
	if err != nil {
		return fmt.Errorf("end watcher %s as %s: %w", w.ID, status, err)
	}

	return nil
}

// locked runs fn in a transaction that holds the locks of the watcher's
// group and of the watcher, in that order, as group closing takes them,
// and that has released the job's unique key, which fences it. fn does
// not run when the watcher ended meanwhile.
func (t *Ticker) locked(ctx context.Context, job db.Job, w db.Watcher, fn func(tx pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, t.opts.Pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		if _, err := queries.LockEventGroup(ctx, w.GroupID); err != nil {
			return fmt.Errorf("lock group %s: %w", w.GroupID, err)
		}

		current, err := queries.LockWatcher(ctx, w.ID)
		if err != nil {
			return fmt.Errorf("lock the watcher: %w", err)
		}

		if current.Status != StatusRunning {
			return nil
		}

		if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
			return fmt.Errorf("release the tick's unique key: %w", err)
		}

		return fn(tx)
	})
	if err != nil {
		return fmt.Errorf("in a transaction: %w", err)
	}

	return nil
}

// endIn sets the running watcher w to a final status in tx and enqueues
// close_group for its group.
func (t *Ticker) endIn(ctx context.Context, tx pgx.Tx, w db.Watcher, status, stopReason string) error {
	ended, err := db.New(tx).EndWatcher(ctx, db.EndWatcherParams{
		Status:     status,
		StopReason: optional(stopReason),
		At:         t.now(),
		ID:         w.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}

	if err != nil {
		return fmt.Errorf("set the watcher %s: %w", status, err)
	}

	stateChanged(ctx, t.log, &w, ended)

	if err := t.opts.CloseGroup(ctx, tx, w.GroupID); err != nil {
		return fmt.Errorf("let group %s close: %w", w.GroupID, err)
	}

	return nil
}

// optional turns "" into NULL.
func optional(s string) *string {
	if s == "" {
		return nil
	}

	return &s
}

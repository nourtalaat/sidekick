package grouper

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// closeKeyPrefix starts the unique keys of a group's close_group jobs.
const closeKeyPrefix = "close_group:"

// resolvedKeySuffix ends the unique key of the close_group job that a
// recovery enqueues, so it does not wait behind the job for the end of the
// window.
const resolvedKeySuffix = ":resolved"

var (
	errNoGroupID     = errors.New("the payload has no group_id")
	errGroupNotFound = errors.New("the group does not exist")
)

// closePayload is the payload of a close_group job.
type closePayload struct {
	GroupID uuid.UUID `json:"group_id"`
}

// CloserOptions set up a Closer. Pool is required.
type CloserOptions struct {
	// Pool is the database the groups and jobs are in.
	Pool *pgxpool.Pool
	// Logger receives one line per closed group, with IDs and the reason
	// only. Nil logs nothing.
	Logger *slog.Logger
	// Watchers records the watchers a resolved group ends. Nil writes and
	// shows the changes, but logs and counts nothing.
	Watchers *watch.Recorder
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Closer runs close_group jobs. Make it with NewCloser and register it for
// queue.KindCloseGroup.
type Closer struct {
	pool     *pgxpool.Pool
	log      *slog.Logger
	watchers *watch.Recorder
	now      func() time.Time
}

var _ queue.Handler = (*Closer)(nil)

// NewCloser returns a Closer.
func NewCloser(opts CloserOptions) *Closer {
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Closer{pool: opts.Pool, log: log, watchers: opts.Watchers, now: now}
}

// Handle runs one close_group job. The job is enqueued for the time a
// group's window ends, and at once when a recovery may have resolved the
// group; it locks the group and decides how it ends:
//
//   - When every alert in the group has recovered, the group closes as
//     resolved, and its running watchers end: alert watchers recover,
//     the others stop with group_resolved.
//   - When the window has ended and no watcher runs, the group closes as
//     window_ended.
//   - When the window has ended but a watcher runs, the group stays open,
//     and the job gives up its unique key, so the end of the last watcher
//     can enqueue it again.
//   - When an event moved the window on since, the job enqueues itself
//     again for the new end.
//
// A group that is closed already is left as it is, so the job is safe to
// run more than once. A payload that does not decode, or a group that
// does not exist, gives up the job.
func (c *Closer) Handle(ctx context.Context, job db.Job) error {
	id, err := decodeClosePayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the close_group payload: %w: %w", queue.ErrGiveUp, err)
	}

	if err := pgx.BeginFunc(ctx, c.pool, func(tx pgx.Tx) error {
		return c.decide(ctx, tx, job, id)
	}); err != nil {
		return fmt.Errorf("group %s: %w", id, err)
	}

	return nil
}

// decide locks the group with id and closes it, leaves it open, or
// enqueues the job again for the end of its window.
func (c *Closer) decide(ctx context.Context, tx pgx.Tx, job db.Job, id uuid.UUID) error {
	queries := db.New(tx)

	grp, err := queries.LockEventGroup(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("%w: %w", queue.ErrGiveUp, errGroupNotFound)
	}

	if err != nil {
		return fmt.Errorf("lock the group: %w", err)
	}

	if grp.Status != statusOpen {
		return nil
	}

	recovered, err := queries.AllAlertsRecovered(ctx, grp.ID)
	if err != nil {
		return fmt.Errorf("check the alerts: %w", err)
	}

	if recovered {
		return endResolved(ctx, c.log, tx, grp.ID, c.now(), c.watchers)
	}

	if grp.WindowUntil != nil && grp.WindowUntil.After(c.now()) {
		if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
			return fmt.Errorf("give up the unique key: %w", err)
		}

		if err := enqueueClose(ctx, tx, grp.ID, *grp.WindowUntil, windowKey(grp.ID)); err != nil {
			return fmt.Errorf("wait for the new end of the window: %w", err)
		}

		return nil
	}

	watching, err := watch.HasRunning(ctx, tx, grp.ID, c.now())
	if err != nil {
		return fmt.Errorf("check the watchers: %w", err)
	}

	if watching {
		// The last watcher to end enqueues the job again, with the same
		// key, which must be free by then.
		if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
			return fmt.Errorf("give up the unique key: %w", err)
		}

		return nil
	}

	if err := closeGroup(ctx, c.log, queries, grp.ID, ClosedWindowEnded); err != nil {
		return fmt.Errorf("end the window: %w", err)
	}

	return nil
}

// EnqueueClose adds a close_group job for the group with id, to run at
// once, in q. A watcher that reached a final state calls it, so a group
// kept open only for its watchers closes when the last one ends. A job
// that is pending or running for the group already counts as added; it
// locks the group, so it sees the watcher's change once q commits.
func EnqueueClose(ctx context.Context, q db.DBTX, id uuid.UUID) error {
	if err := enqueueClose(ctx, q, id, time.Time{}, windowKey(id)); err != nil {
		return fmt.Errorf("group %s: %w", id, err)
	}

	return nil
}

// enqueueClose adds a close_group job of the group with id, with key, to
// run at runAt, or at once for the zero time. A job that is already
// pending or running with key counts as added; the one for the window
// enqueues itself again when the window has moved on.
func enqueueClose(ctx context.Context, q db.DBTX, id uuid.UUID, runAt time.Time, key string) error {
	payload, err := json.Marshal(closePayload{GroupID: id})
	if err != nil {
		return fmt.Errorf("encode the close_group payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:      queue.KindCloseGroup,
		Payload:   payload,
		UniqueKey: key,
		RunAt:     runAt,
	})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue close_group: %w", err)
	}

	return nil
}

// closeGroup closes the open group with id for reason.
func closeGroup(ctx context.Context, log *slog.Logger, queries *db.Queries, id uuid.UUID, reason string) error {
	if _, err := queries.CloseEventGroup(ctx, db.CloseEventGroupParams{ClosedReason: reason, ID: id}); err != nil {
		return fmt.Errorf("close the group: %w", err)
	}

	log.InfoContext(ctx, "group closed", "group_id", id.String(), "reason", reason)

	return nil
}

// windowKey returns the unique key of the close_group job for the end of
// the window of the group with id.
func windowKey(id uuid.UUID) string {
	return closeKeyPrefix + id.String()
}

// resolvedKey returns the unique key of the close_group job that a
// recovery in the group with id enqueues.
func resolvedKey(id uuid.UUID) string {
	return closeKeyPrefix + id.String() + resolvedKeySuffix
}

// decodeClosePayload returns the group ID of a close_group payload.
func decodeClosePayload(raw []byte) (uuid.UUID, error) {
	var p closePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return uuid.Nil, fmt.Errorf("decode: %w", err)
	}

	if p.GroupID == uuid.Nil {
		return uuid.Nil, errNoGroupID
	}

	return p.GroupID, nil
}

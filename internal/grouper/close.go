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
)

// closeKeyPrefix starts the unique key of a group's close_group job.
const closeKeyPrefix = "close_group:"

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
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Closer runs close_group jobs. Make it with NewCloser and register it for
// queue.KindCloseGroup.
type Closer struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
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

	return &Closer{pool: opts.Pool, log: log, now: now}
}

// Handle runs one close_group job. The job is enqueued for the time a
// group's window ends; it locks the group and decides how it ends:
//
//   - When the window has ended and no watcher runs, the group closes as
//     window_ended.
//   - When the window has ended but a watcher runs, the group stays open;
//     the watcher's end enqueues the job again.
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

	if grp.WindowUntil != nil && grp.WindowUntil.After(c.now()) {
		if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
			return fmt.Errorf("give up the unique key: %w", err)
		}

		if err := enqueueClose(ctx, tx, grp.ID, *grp.WindowUntil); err != nil {
			return fmt.Errorf("wait for the new end of the window: %w", err)
		}

		return nil
	}

	if watchersRunning(grp) {
		return nil
	}

	if err := closeGroup(ctx, c.log, queries, grp.ID, ClosedWindowEnded); err != nil {
		return fmt.Errorf("end the window: %w", err)
	}

	return nil
}

// enqueueClose adds the close_group job of the group with id, to run at
// runAt. A job that is already pending or running for the group counts as
// added; it enqueues itself again when the window has moved on.
func enqueueClose(ctx context.Context, q db.DBTX, id uuid.UUID, runAt time.Time) error {
	payload, err := json.Marshal(closePayload{GroupID: id})
	if err != nil {
		return fmt.Errorf("encode the close_group payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, q, queue.NewJob{
		Kind:      queue.KindCloseGroup,
		Payload:   payload,
		UniqueKey: closeKeyPrefix + id.String(),
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

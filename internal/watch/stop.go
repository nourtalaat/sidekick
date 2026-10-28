package watch

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// errMissingStopperOption is returned by NewStopper without a required
// option.
var errMissingStopperOption = errors.New("new watch stopper: Pool and CloseGroup are required")

// StopperOptions set up a Stopper. Pool and CloseGroup are required.
type StopperOptions struct {
	// Pool is the database the watchers, groups and jobs are in.
	Pool *pgxpool.Pool
	// CloseGroup enqueues the close_group job of a group in q, so a group
	// kept open for its watchers can close once the stopped one ends.
	CloseGroup func(ctx context.Context, q db.DBTX, groupID uuid.UUID) error
	// Recorder records each stop. Nil writes and shows it, but logs and
	// counts nothing.
	Recorder *Recorder
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
}

// Stopper stops watchers for responders, such as from the Stop watching
// button in Slack. Make it with NewStopper; it is safe for concurrent use.
type Stopper struct {
	opts StopperOptions
	now  func() time.Time
}

// NewStopper returns a Stopper.
func NewStopper(opts StopperOptions) (*Stopper, error) {
	if opts.Pool == nil || opts.CloseGroup == nil {
		return nil, errMissingStopperOption
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	return &Stopper{opts: opts, now: now}, nil
}

// Stop stops the running watcher with id for the responder by, with
// stop_reason responder, and returns it. In one transaction it locks the
// watcher's group and then the watcher, as a tick does, records the change
// and enqueues close_group for the group; the watcher's next tick sees the
// status and ends. It reports false, and changes nothing, for a watcher
// that does not exist or has ended already.
func (s *Stopper) Stop(ctx context.Context, id uuid.UUID, by output.Person) (db.Watcher, bool, error) {
	var (
		stopped db.Watcher
		ok      bool
	)

	err := pgx.BeginFunc(ctx, s.opts.Pool, func(tx pgx.Tx) error {
		var err error

		stopped, ok, err = s.stop(ctx, tx, id, by)
		if err != nil {
			return fmt.Errorf("in a transaction: %w", err)
		}

		return nil
	})
	if err != nil {
		return db.Watcher{}, false, fmt.Errorf("stop watcher %s: %w", id, err)
	}

	return stopped, ok, nil
}

// stop stops the watcher with id in tx.
func (s *Stopper) stop(ctx context.Context, tx pgx.Tx, id uuid.UUID, by output.Person) (db.Watcher, bool, error) {
	queries := db.New(tx)

	w, err := queries.GetWatcher(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Watcher{}, false, nil
	}

	if err != nil {
		return db.Watcher{}, false, fmt.Errorf("load the watcher: %w", err)
	}

	if _, err := queries.LockEventGroup(ctx, w.GroupID); err != nil {
		return db.Watcher{}, false, fmt.Errorf("lock group %s: %w", w.GroupID, err)
	}

	current, err := queries.LockWatcher(ctx, id)
	if err != nil {
		return db.Watcher{}, false, fmt.Errorf("lock the watcher: %w", err)
	}

	if current.Status != StatusRunning {
		return current, false, nil
	}

	ended, err := queries.EndWatcher(ctx, db.EndWatcherParams{
		Status:             StatusStopped,
		StopReason:         optional(StopResponder),
		StoppedBySlackUser: optional(by.SlackUserID),
		At:                 s.now(),
		ID:                 id,
	})
	if err != nil {
		return db.Watcher{}, false, fmt.Errorf("stop the watcher: %w", err)
	}

	if err := s.opts.Recorder.changed(ctx, tx, &current, ended); err != nil {
		return db.Watcher{}, false, fmt.Errorf("record the stop: %w", err)
	}

	if err := s.opts.CloseGroup(ctx, tx, w.GroupID); err != nil {
		return db.Watcher{}, false, fmt.Errorf("let group %s close: %w", w.GroupID, err)
	}

	return ended, true, nil
}

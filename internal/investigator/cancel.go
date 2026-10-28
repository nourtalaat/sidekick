package investigator

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

const (
	// cancelListenRetry is the wait after the cancel listener's connection
	// failed, before it listens again. Runs still see a Stop at their next
	// step meanwhile.
	cancelListenRetry = 5 * time.Second
	// cancelCloseTimeout bounds closing the listener's connection.
	cancelCloseTimeout = 5 * time.Second
)

// errCancelled is the cause of a run's context when a responder stops it.
var errCancelled = errors.New("a responder stopped the investigation")

// ActionCancelRequested is the action of the audit entry of a request to
// stop a run.
const ActionCancelRequested = "investigation.cancel_requested"

// Requester is who asked for something: a Sidekick user, or a Slack user
// who has none yet. At least one is set.
type Requester struct {
	UserID      *uuid.UUID
	SlackUserID string
	// Token says UserID is the ID of an API token, not of a user.
	Token bool
}

// actorKind is the kind of actor the requester is in an audit entry.
func (r Requester) actorKind() string {
	if r.Token {
		return audit.ActorToken
	}

	return audit.ActorUser
}

// actorID names the requester in an audit entry: the Sidekick user ID, or
// the Slack user ID of someone with none yet.
func (r Requester) actorID() string {
	if r.UserID != nil {
		return r.UserID.String()
	}

	return r.SlackUserID
}

// RequestCancel asks the run of a queued or running investigation to stop,
// and tells every replica once q's transaction commits. The worker that
// owns the run then cancels the model or tool call in flight, and the run
// ends as cancelled with a report built from its saved steps. It returns
// false, and changes nothing, when the investigation has ended or a stop
// was asked for already.
func RequestCancel(ctx context.Context, q db.DBTX, id uuid.UUID, by Requester) (bool, error) {
	queries := db.New(q)

	_, err := queries.RequestInvestigationCancel(ctx, db.RequestInvestigationCancelParams{
		UserID:      by.UserID,
		SlackUserID: optional(by.SlackUserID),
		ID:          id,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}

	if err != nil {
		return false, fmt.Errorf("ask to stop investigation %s: %w", id, err)
	}

	if err := queries.NotifyInvestigationCancel(ctx, id.String()); err != nil {
		return false, fmt.Errorf("notify the stop of investigation %s: %w", id, err)
	}

	err = auditor.Write(ctx, q, audit.Entry{
		ActorKind:       by.actorKind(),
		ActorID:         by.actorID(),
		Action:          ActionCancelRequested,
		TargetKind:      auditTargetKind,
		TargetID:        id.String(),
		InvestigationID: &id,
	})
	if err != nil {
		return false, fmt.Errorf("audit the stop of investigation %s: %w", id, err)
	}

	return true, nil
}

// Canceller stops the runs on this replica that a responder asked to stop.
// It listens for the notifications RequestCancel sends, from any replica,
// and cancels the context of the matching run. Make it with NewCanceller,
// pass it in Options.Canceller and call Run.
type Canceller struct {
	pool  *pgxpool.Pool
	log   *slog.Logger
	retry time.Duration

	mu sync.Mutex
	// runs holds the cancel function of each run on this replica, by
	// investigation and then by the run's fence.
	runs map[uuid.UUID]map[int64]context.CancelCauseFunc
}

// NewCanceller returns a Canceller that listens on a connection from pool.
// A nil logger logs nothing.
func NewCanceller(pool *pgxpool.Pool, log *slog.Logger) *Canceller {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Canceller{pool: pool, log: log, retry: cancelListenRetry, runs: map[uuid.UUID]map[int64]context.CancelCauseFunc{}}
}

// Run listens for stops until ctx ends, and returns nil then. When its
// connection fails it listens again after a pause.
func (c *Canceller) Run(ctx context.Context) error {
	for ctx.Err() == nil {
		if err := c.listen(ctx); err != nil && ctx.Err() == nil {
			c.log.WarnContext(ctx, "listening for stopped investigations failed; runs see a stop at their next step until it works again", "error", err.Error())
		}

		select {
		case <-ctx.Done():
		case <-time.After(c.retry):
		}
	}

	return nil
}

// listen holds one connection that listens for stops. Once it listens, it
// stops the runs that were asked to stop while nothing listened.
func (c *Canceller) listen(ctx context.Context) error {
	pooled, err := c.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire a connection: %w", err)
	}

	// The connection leaves the pool, so it never goes back still
	// listening.
	conn := pooled.Hijack()

	defer func() {
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cancelCloseTimeout)
		defer cancel()

		if err := conn.Close(closeCtx); err != nil {
			c.log.WarnContext(ctx, "closing the cancel listener's connection failed", "error", err.Error())
		}
	}()

	queries := db.New(conn)

	if err := queries.ListenInvestigationCancel(ctx); err != nil {
		return fmt.Errorf("listen: %w", err)
	}

	if err := c.catchUp(ctx, queries); err != nil {
		return fmt.Errorf("catch up: %w", err)
	}

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for a notification: %w", err)
		}

		id, err := uuid.Parse(n.Payload)
		if err != nil {
			c.log.WarnContext(ctx, "ignoring a stop notification that is not an investigation ID")

			continue
		}

		c.cancel(id)
	}
}

// catchUp stops the runs on this replica that were asked to stop while
// nothing listened.
func (c *Canceller) catchUp(ctx context.Context, queries *db.Queries) error {
	c.mu.Lock()
	ids := slices.Collect(maps.Keys(c.runs))
	c.mu.Unlock()

	if len(ids) == 0 {
		return nil
	}

	stopped, err := queries.ListCancelRequested(ctx, ids)
	if err != nil {
		return fmt.Errorf("list the stopped investigations: %w", err)
	}

	for _, id := range stopped {
		c.cancel(id)
	}

	return nil
}

// cancel cancels every run of the investigation with id on this replica.
func (c *Canceller) cancel(id uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, cancel := range c.runs[id] {
		cancel(errCancelled)
	}
}

// watch registers the cancel function of the run of investigation id with
// fence, and returns the function that removes it. A nil Canceller watches
// nothing.
func (c *Canceller) watch(id uuid.UUID, fence int64, cancel context.CancelCauseFunc) (unwatch func()) {
	if c == nil {
		return func() {}
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.runs[id] == nil {
		c.runs[id] = map[int64]context.CancelCauseFunc{}
	}

	c.runs[id][fence] = cancel

	return func() {
		c.mu.Lock()
		defer c.mu.Unlock()

		delete(c.runs[id], fence)

		if len(c.runs[id]) == 0 {
			delete(c.runs, id)
		}
	}
}

package grouper

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// SpanName is the name of the span of grouping one event.
const SpanName = "grouper.group"

// tracerName names the tracer of this package.
const tracerName = "github.com/nourtalaat/sidekick/internal/grouper"

// Span attributes.
const (
	attrRoute   = "sidekick.route"
	attrGroupID = "sidekick.group.id"
)

// statusOpen is the event_groups.status of an open group.
const statusOpen = "open"

// The values of event_groups.closed_reason.
const (
	// ClosedResolved is a group whose alerts have all recovered.
	ClosedResolved = "resolved"
	// ClosedWindowEnded is a group that had no new event for its window
	// and runs no watcher.
	ClosedWindowEnded = "window_ended"
)

// maxOpenTries is how many times Group looks for an open group and tries to
// open one. Each try that fails means another transaction opened or closed
// the fingerprint's group in between; two in a row are rare already.
const maxOpenTries = 3

var (
	errNotRouted = errors.New("the event is not routed")
	errContended = errors.New("the fingerprint's group kept changing")
)

// Options set up a Grouper.
type Options struct {
	// Logger receives one line per opened, joined or closed group, with
	// IDs only, never event text. Nil logs nothing.
	Logger *slog.Logger
	// TracerProvider makes the tracer of the grouper.group spans. Nil
	// means the global tracer provider.
	TracerProvider trace.TracerProvider
}

// Grouper puts events into groups. Make it with New. It holds no state of
// its own, so one Grouper serves every goroutine.
type Grouper struct {
	log    *slog.Logger
	tracer trace.Tracer
}

// Route is what grouping needs to know about the route of an event.
type Route struct {
	// Name is the route's name, which a new group records.
	Name string
	// Window is how long a group stays open after its newest event.
	Window time.Duration
	// FingerprintLabels are the labels the fingerprint of an alert is made
	// from; empty means the fingerprint the intake stored.
	FingerprintLabels []string
}

// Result is the group an event went to.
type Result struct {
	GroupID uuid.UUID
	// InvestigationID is the group's investigation. When Opened is set,
	// the caller creates it, with this ID, in the same transaction.
	InvestigationID uuid.UUID
	// Opened means the event opened a new group.
	Opened bool
}

// New returns a Grouper.
func New(opts Options) *Grouper {
	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Grouper{log: log, tracer: tp.Tracer(tracerName)}
}

// Group puts ev, which the caller has just marked routed in tx, into a
// group of rt inside a grouper.group span: it joins the open group with
// its fingerprint, or opens a new one. A joined event goes to the inbox of
// the group's investigation, and the investigation's outputs are refreshed
// to show the new event count. When the result is Opened, the caller must
// create the investigation with Result.InvestigationID in tx.
func (g *Grouper) Group(ctx context.Context, tx pgx.Tx, ev db.Event, rt Route) (res Result, err error) {
	ctx, span := g.tracer.Start(ctx, SpanName, trace.WithAttributes(attribute.String(attrRoute, rt.Name)))

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "grouping failed")
		} else {
			span.SetAttributes(attribute.String(attrGroupID, res.GroupID.String()))
		}

		span.End()
	}()

	e, err := store.EventOf(ev)
	if err != nil {
		return Result{}, fmt.Errorf("read event %s: %w", ev.ID, err)
	}

	fp := fingerprint(e, rt.FingerprintLabels)

	for range maxOpenTries {
		res, done, err := g.tryGroup(ctx, tx, ev, rt, fp)
		if err != nil {
			return Result{}, fmt.Errorf("group event %s: %w", ev.ID, err)
		}

		if done {
			return res, nil
		}
	}

	return Result{}, fmt.Errorf("group event %s: %w", ev.ID, errContended)
}

// tryGroup joins the open group with fingerprint fp, or opens one. It
// reports false when another transaction opened a group for fp first,
// so the caller tries again and joins that one.
func (g *Grouper) tryGroup(ctx context.Context, tx pgx.Tx, ev db.Event, rt Route, fp string) (Result, bool, error) {
	queries := db.New(tx)

	open, err := queries.LockOpenEventGroup(ctx, fp)

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return g.open(ctx, tx, ev, rt, fp)
	case err != nil:
		return Result{}, false, fmt.Errorf("find the open group: %w", err)
	}

	watching, err := watch.HasRunning(ctx, tx, open.ID, time.Now())
	if err != nil {
		return Result{}, false, fmt.Errorf("group %s: %w", open.ID, err)
	}

	if joinable(open, ev, rt, watching) {
		res, err := g.join(ctx, tx, open, ev, rt)
		if err != nil {
			return Result{}, false, fmt.Errorf("join group %s: %w", open.ID, err)
		}

		return res, true, nil
	}

	if err := closeGroup(ctx, g.log, queries, open.ID, ClosedWindowEnded); err != nil {
		return Result{}, false, fmt.Errorf("close group %s: %w", open.ID, err)
	}

	return g.open(ctx, tx, ev, rt, fp)
}

// join adds ev to the open group grp, writes it to the inbox of the group's
// investigation and refreshes the investigation's outputs.
func (g *Grouper) join(ctx context.Context, tx pgx.Tx, grp db.EventGroup, ev db.Event, rt Route) (Result, error) {
	queries := db.New(tx)

	joined, err := queries.JoinEventGroup(ctx, db.JoinEventGroupParams{
		ReceivedAt:  ev.ReceivedAt,
		WindowUntil: ev.ReceivedAt.Add(rt.Window),
		ID:          grp.ID,
	})
	if err != nil {
		return Result{}, fmt.Errorf("count the event: %w", err)
	}

	if err := markGrouped(ctx, queries, ev.ID, grp.ID); err != nil {
		return Result{}, fmt.Errorf("record the group: %w", err)
	}

	if err := enqueueClose(ctx, tx, grp.ID, *joined.WindowUntil, windowKey(grp.ID)); err != nil {
		return Result{}, fmt.Errorf("plan the end of the window: %w", err)
	}

	res := Result{GroupID: grp.ID}

	if grp.InvestigationID != nil {
		res.InvestigationID = *grp.InvestigationID

		if err := investigator.AddEventToInbox(ctx, tx, res.InvestigationID, investigator.InboxNewEvent, ev.ID); err != nil {
			return Result{}, fmt.Errorf("tell the investigation: %w", err)
		}

		if err := publish.Refresh(ctx, tx, res.InvestigationID); err != nil {
			return Result{}, fmt.Errorf("refresh the outputs: %w", err)
		}
	}

	g.log.DebugContext(ctx, "event joined a group", "event_id", ev.ID.String(), "group_id", grp.ID.String(), "events", joined.EventCount)

	return res, nil
}

// open opens a group for ev with fingerprint fp. It reports false, and
// opens nothing, when a group with fp is open already.
func (g *Grouper) open(ctx context.Context, tx pgx.Tx, ev db.Event, rt Route, fp string) (Result, bool, error) {
	queries := db.New(tx)
	res := Result{GroupID: ids.New(), InvestigationID: ids.New(), Opened: true}

	_, err := queries.OpenEventGroup(ctx, db.OpenEventGroupParams{
		ID:              res.GroupID,
		Fingerprint:     fp,
		Route:           rt.Name,
		Service:         ev.Service,
		LastEventAt:     ev.ReceivedAt,
		WindowUntil:     ev.ReceivedAt.Add(rt.Window),
		InvestigationID: &res.InvestigationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return Result{}, false, nil
	}

	if err != nil {
		return Result{}, false, fmt.Errorf("open a group: %w", err)
	}

	if err := markGrouped(ctx, queries, ev.ID, res.GroupID); err != nil {
		return Result{}, false, fmt.Errorf("record the group: %w", err)
	}

	if err := enqueueClose(ctx, tx, res.GroupID, ev.ReceivedAt.Add(rt.Window), windowKey(res.GroupID)); err != nil {
		return Result{}, false, fmt.Errorf("plan the end of the window: %w", err)
	}

	g.log.DebugContext(ctx, "event opened a group", "event_id", ev.ID.String(), "group_id", res.GroupID.String())

	return res, true, nil
}

// joinable reports whether ev joins the open group grp: it arrived before
// the group's window ended, or watching says a watcher keeps the group
// open. A group without a window, opened before groups had one, ends
// rt.Window after its newest event.
func joinable(grp db.EventGroup, ev db.Event, rt Route, watching bool) bool {
	end := grp.LastEventAt.Add(rt.Window)
	if grp.WindowUntil != nil {
		end = *grp.WindowUntil
	}

	return ev.ReceivedAt.Before(end) || watching
}

// markGrouped records that the event with id is in the group with groupID.
func markGrouped(ctx context.Context, queries *db.Queries, id, groupID uuid.UUID) error {
	marked, err := queries.MarkEventGrouped(ctx, db.MarkEventGroupedParams{GroupID: &groupID, ID: id})
	if err != nil {
		return fmt.Errorf("mark event %s grouped: %w", id, err)
	}

	if marked == 0 {
		return fmt.Errorf("mark event %s grouped: %w", id, errNotRouted)
	}

	return nil
}

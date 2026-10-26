package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/ids"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// SpanName is the name of the span of one route_event job.
const SpanName = "router.route"

// tracerName names the tracer of this package.
const tracerName = "github.com/nourtalaat/sidekick/internal/router"

// Span attributes.
const (
	attrRoute   = "sidekick.route"
	attrGroupID = "sidekick.group.id"
)

// statusReceived is the status of an event that intake stored and that is
// waiting to be routed.
const statusReceived = "received"

var (
	errNoEventID     = errors.New("the payload has no event_id")
	errEventNotFound = errors.New("the event does not exist")
	errNoRoute       = errors.New("no route matches, and there is no default route")
)

// Options set up a Router. Config and Pool are required.
type Options struct {
	// Config holds the routes, loaded with its strategies.
	Config *config.Config
	// ModelSettings are the shared settings of each built connector, for
	// the model IDs stored with each investigation. A connector without
	// them is stored without a model ID.
	ModelSettings map[string]model.Settings
	// Pool is the database the events, groups, investigations and jobs
	// are in.
	Pool *pgxpool.Pool
	// Logger receives one line per routed event, with IDs and the route
	// only, never event text. Nil logs nothing.
	Logger *slog.Logger
	// TracerProvider makes the tracer of the router.route spans. Nil means
	// the global tracer provider.
	TracerProvider trace.TracerProvider
}

// Router handles route_event jobs. Make it with New and register it for
// queue.KindRouteEvent.
type Router struct {
	routes []route
	pool   *pgxpool.Pool
	log    *slog.Logger
	tracer trace.Tracer
}

var _ queue.Handler = (*Router)(nil)

// routeEventPayload is the payload of a route_event job, as intake writes
// it.
type routeEventPayload struct {
	EventID uuid.UUID `json:"event_id"`
}

// opened is what routing an event created.
type opened struct {
	groupID         uuid.UUID
	investigationID uuid.UUID
}

// New prepares the routes of opts.Config.
func New(opts Options) (*Router, error) {
	routes, err := newRoutes(opts.Config, opts.ModelSettings)
	if err != nil {
		return nil, fmt.Errorf("prepare the routes: %w", err)
	}

	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Router{routes: routes, pool: opts.Pool, log: log, tracer: tp.Tracer(tracerName)}, nil
}

// Handle routes the event of one route_event job inside a router.route
// span, a child of the span that enqueued the job. In one transaction it
// sets the event to routed with its route and a new group, opens that
// group, creates a queued investigation for it, enqueues its investigate
// job and enqueues the first publish job for each output of the route.
//
// It is safe to run more than once: an event that is no longer received,
// because it was routed already or intake skipped it, is left as it is,
// and the change to routed only applies to a received event, so two runs
// at the same time still open one group. A payload that does not decode,
// or an event that does not exist, gives up the job.
func (r *Router) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := r.tracer.Start(queue.TraceContext(ctx, job), SpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "routing failed")
		}

		span.End()
	}()

	eventID, err := decodePayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the route_event payload: %w: %w", queue.ErrGiveUp, err)
	}

	log := r.log.With("event_id", eventID.String(), "job_id", job.ID)

	ev, rt, waiting, err := r.match(ctx, eventID)
	if err != nil {
		return fmt.Errorf("event %s: %w", eventID, err)
	}

	if !waiting {
		log.DebugContext(ctx, "event not routed: it is not waiting to be routed", "status", ev.Status)

		return nil
	}

	span.SetAttributes(attribute.String(attrRoute, rt.name))
	log = log.With("route", rt.name)

	o, routed, err := r.open(ctx, ev, rt)
	if err != nil {
		return fmt.Errorf("route event %s: %w", eventID, err)
	}

	if !routed {
		log.DebugContext(ctx, "event not routed: another run routed it first")

		return nil
	}

	span.SetAttributes(attribute.String(attrGroupID, o.groupID.String()))
	log.InfoContext(ctx, "event routed", "group_id", o.groupID.String(), "investigation_id", o.investigationID.String())

	return nil
}

// match loads the event with id and picks its route. It reports false for
// an event that is not waiting to be routed. Errors that wrap
// queue.ErrGiveUp mean a retry cannot help.
func (r *Router) match(ctx context.Context, id uuid.UUID) (ev db.Event, rt route, waiting bool, err error) {
	ev, err = db.New(r.pool).GetEvent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Event{}, route{}, false, fmt.Errorf("%w: %w", queue.ErrGiveUp, errEventNotFound)
	}

	if err != nil {
		return db.Event{}, route{}, false, fmt.Errorf("load the event: %w", err)
	}

	if ev.Status != statusReceived || ev.Kind == string(event.KindResolved) {
		return ev, route{}, false, nil
	}

	c, err := candidateOf(ev)
	if err != nil {
		return db.Event{}, route{}, false, fmt.Errorf("%w: %w", queue.ErrGiveUp, err)
	}

	rt, ok := pick(r.routes, c)
	if !ok {
		return db.Event{}, route{}, false, fmt.Errorf("%w: %w", queue.ErrGiveUp, errNoRoute)
	}

	return ev, rt, true, nil
}

// open routes ev to rt in one transaction: it marks the event routed,
// opens its group, creates the investigation, enqueues the investigate
// job and opens the route's outputs, so each shows "Looking into it" at
// once. It reports false, and writes nothing, when the event is no longer
// received.
func (r *Router) open(ctx context.Context, ev db.Event, rt route) (o opened, routed bool, err error) {
	o = opened{groupID: ids.New(), investigationID: ids.New()}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return opened{}, false, fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	queries := db.New(tx)

	marked, err := queries.MarkEventRouted(ctx, db.MarkEventRoutedParams{ID: ev.ID, Route: &rt.name, GroupID: &o.groupID})
	if err != nil {
		return opened{}, false, fmt.Errorf("mark the event routed: %w", err)
	}

	if marked == 0 {
		return opened{}, false, nil
	}

	if err := queries.InsertEventGroup(ctx, db.InsertEventGroupParams{
		ID:              o.groupID,
		Fingerprint:     ev.Fingerprint,
		Route:           rt.name,
		Service:         ev.Service,
		LastEventAt:     ev.ReceivedAt,
		InvestigationID: &o.investigationID,
	}); err != nil {
		return opened{}, false, fmt.Errorf("open the group: %w", err)
	}

	if err := queries.InsertInvestigation(ctx, db.InsertInvestigationParams{
		ID:              o.investigationID,
		GroupID:         o.groupID,
		Route:           rt.name,
		Strategy:        &rt.strategy,
		StrategyVersion: &rt.strategyVersion,
		Mode:            string(action.EffectiveMode(rt.mode, trust(ev), false)),
		Status:          investigator.StatusQueued,
		Connectors:      rt.connectors,
		Budget:          rt.budget,
	}); err != nil {
		return opened{}, false, fmt.Errorf("create the investigation: %w", err)
	}

	if err := enqueueInvestigate(ctx, tx, rt.name, o); err != nil {
		return opened{}, false, fmt.Errorf("investigation %s: %w", o.investigationID, err)
	}

	if err := publish.Open(ctx, tx, o.investigationID, rt.outputs); err != nil {
		return opened{}, false, fmt.Errorf("investigation %s: open its outputs: %w", o.investigationID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return opened{}, false, fmt.Errorf("commit: %w", err)
	}

	return o, true, nil
}

// enqueueInvestigate adds the investigate job of the group in o. A job
// that is already pending or running for the group counts as added.
func enqueueInvestigate(ctx context.Context, tx pgx.Tx, routeName string, o opened) error {
	payload, err := json.Marshal(investigator.Payload{InvestigationID: o.investigationID, GroupID: o.groupID})
	if err != nil {
		return fmt.Errorf("encode the investigate payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, tx, queue.NewJob{
		Kind:      queue.KindInvestigate,
		Route:     routeName,
		Payload:   payload,
		UniqueKey: investigator.UniqueKey(o.groupID),
	})
	if err != nil && !errors.Is(err, queue.ErrDuplicate) {
		return fmt.Errorf("enqueue investigate: %w", err)
	}

	return nil
}

// decodePayload returns the event ID of a route_event payload.
func decodePayload(raw []byte) (uuid.UUID, error) {
	var p routeEventPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return uuid.Nil, fmt.Errorf("decode: %w", err)
	}

	if p.EventID == uuid.Nil {
		return uuid.Nil, errNoEventID
	}

	return p.EventID, nil
}

// trust returns the trust intake recorded for ev.
func trust(ev db.Event) event.Trust {
	if ev.Trusted {
		return event.Trusted
	}

	return event.Untrusted
}

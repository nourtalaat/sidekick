package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/investigator"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/publish"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/watch"
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
	// Metrics is where the router registers sidekick_events_skipped_total.
	// Nil records nothing.
	Metrics prometheus.Registerer
	// TracerProvider makes the tracer of the router.route and
	// grouper.group spans. Nil means the global tracer provider.
	TracerProvider trace.TracerProvider
	// Catalog says which services depend on each other, for routes with
	// grouping.relatedWindow. Nil means it knows of no dependencies.
	Catalog *grouper.Catalog
	// WatchPolicy replaces the watcher policy of Config, which holds the
	// watchers each route's groups start. The benchmark gives an empty
	// one, so its runs start no watchers. Nil means Config.WatchPolicy().
	WatchPolicy *watch.Policy
	// Watchers records the watchers the router starts. Nil writes and
	// shows each start, but logs and counts nothing.
	Watchers *watch.Recorder
}

// Router handles route_event jobs. Make it with New and register it for
// queue.KindRouteEvent.
type Router struct {
	// routes are the routes in effect, in the order they are tried.
	// SetRoutes replaces them while events are routed.
	routes        atomic.Pointer[[]route]
	cfg           *config.Config
	modelSettings map[string]model.Settings
	watch         watch.Policy
	watchers      *watch.Recorder
	pool          *pgxpool.Pool
	grouper       *grouper.Grouper
	log           *slog.Logger
	tracer        trace.Tracer
	// globalRate is budgets.rateLimit, nil for none.
	globalRate *config.RateLimit
	// skippedEvents counts the events a rate limit skipped; nil counts
	// none.
	skippedEvents *prometheus.CounterVec
	// now is the clock of the rate limits. Tests replace it.
	now func() time.Time
}

var _ queue.Handler = (*Router)(nil)

// routeEventPayload is the payload of a route_event job, as intake writes
// it.
type routeEventPayload struct {
	EventID uuid.UUID `json:"event_id"`
}

// New prepares the routes of opts.Config.
func New(opts Options) (*Router, error) {
	policy := opts.Config.WatchPolicy()
	if opts.WatchPolicy != nil {
		policy = *opts.WatchPolicy
	}

	routes, err := newRoutes(opts.Config, opts.ModelSettings, policy)
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

	r := &Router{
		cfg:           opts.Config,
		modelSettings: opts.ModelSettings,
		watch:         policy,
		watchers:      opts.Watchers,
		pool:          opts.Pool,
		grouper:       grouper.New(grouper.Options{Logger: log, TracerProvider: tp, Catalog: opts.Catalog}),
		log:           log,
		tracer:        tp.Tracer(tracerName),
		globalRate:    opts.Config.Budgets.RateLimit,
		now:           time.Now,
	}
	r.routes.Store(&routes)

	if opts.Metrics != nil {
		r.skippedEvents, err = investigator.NewSkippedEvents(opts.Metrics)
		if err != nil {
			return nil, fmt.Errorf("set up the router metrics: %w", err)
		}
	}

	return r, nil
}

// SetRoutes replaces the routes in effect with ordered, which must already
// be in the order they are tried, as rules.Effective lists them. Events
// routed after it returns use the new routes. On an error the routes stay
// as they were.
func (r *Router) SetRoutes(ordered []config.Route) error {
	routes, err := compileRoutes(r.cfg, ordered, r.modelSettings, r.watch)
	if err != nil {
		return fmt.Errorf("prepare the routes: %w", err)
	}

	r.routes.Store(&routes)

	return nil
}

// Handle routes the event of one route_event job inside a router.route
// span, a child of the span that enqueued the job. After picking the
// route, it checks the global and the route's rate limits, which can hold
// the event as delayed or skipped. Then, in one transaction, it sets the
// event to routed with its route and hands it to the grouper,
// which joins it to the open group with its fingerprint or opens a new
// one. For a new group it creates a queued investigation, enqueues its
// investigate job, enqueues the first publish job for each output of the
// route and starts the route's and its strategy's watchers that match the
// event. A recovery event is not routed: the grouper links it to the
// group its alert is in.
//
// It is safe to run more than once: an event that is no longer received,
// because it was routed already or intake skipped it, is left as it is,
// and the change to routed or grouped only applies to a received event,
// so two runs at the same time still group it once. A payload that does
// not decode, or an event that does not exist, gives up the job.
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

	ev, err := r.load(ctx, eventID)
	if err != nil {
		return fmt.Errorf("event %s: %w", eventID, err)
	}

	switch {
	case ev.Status != statusReceived && ev.Status != statusDelayed:
		log.DebugContext(ctx, "event not routed: it is not waiting to be routed", "status", ev.Status)

		return nil
	case ev.Kind == string(event.KindResolved):
		return r.handleRecovery(ctx, span, log, ev)
	default:
		return r.handleEvent(ctx, span, log, job, ev)
	}
}

// handleEvent picks the route of ev, checks the rate limits, and routes
// it when they let it through. job is the event's route_event job, which
// a delayed event moves to later.
func (r *Router) handleEvent(ctx context.Context, span trace.Span, log *slog.Logger, job db.Job, ev db.Event) error {
	rt, err := r.match(ev)
	if err != nil {
		return fmt.Errorf("event %s: %w", ev.ID, err)
	}

	span.SetAttributes(attribute.String(attrRoute, rt.name))
	log = log.With("route", rt.name)

	admitted, err := r.admit(ctx, job, ev, rt, log)
	if err != nil {
		return fmt.Errorf("event %s: %w", ev.ID, err)
	}

	if !admitted {
		return nil
	}

	res, routed, err := r.route(ctx, ev, rt)
	if err != nil {
		return fmt.Errorf("route event %s: %w", ev.ID, err)
	}

	if !routed {
		log.DebugContext(ctx, "event not routed: another run routed it first")

		return nil
	}

	span.SetAttributes(attribute.String(attrGroupID, res.GroupID.String()))
	log.InfoContext(ctx, "event routed",
		"group_id", res.GroupID.String(), "investigation_id", res.InvestigationID.String(), "opened_group", res.Opened)

	return nil
}

// handleRecovery links the recovery event ev to the group its alert is in,
// in one transaction.
func (r *Router) handleRecovery(ctx context.Context, span trace.Span, log *slog.Logger, ev db.Event) error {
	var (
		res    grouper.Result
		linked bool
	)

	err := pgx.BeginFunc(ctx, r.pool, func(tx pgx.Tx) error {
		var err error

		res, linked, err = r.grouper.Recover(ctx, tx, ev)
		if err != nil {
			return fmt.Errorf("recover: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("link recovery event %s: %w", ev.ID, err)
	}

	if !linked {
		log.InfoContext(ctx, "recovery event not linked: no group has its alert, or another run linked it first")

		return nil
	}

	span.SetAttributes(attribute.String(attrGroupID, res.GroupID.String()))
	log.InfoContext(ctx, "recovery event linked", "group_id", res.GroupID.String(), "investigation_id", res.InvestigationID.String())

	return nil
}

// load returns the event with id. An event that does not exist gives an
// error that wraps queue.ErrGiveUp.
func (r *Router) load(ctx context.Context, id uuid.UUID) (db.Event, error) {
	ev, err := db.New(r.pool).GetEvent(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Event{}, fmt.Errorf("%w: %w", queue.ErrGiveUp, errEventNotFound)
	}

	if err != nil {
		return db.Event{}, fmt.Errorf("load the event: %w", err)
	}

	return ev, nil
}

// match picks the route of ev. Errors wrap queue.ErrGiveUp, since a retry
// cannot help.
func (r *Router) match(ev db.Event) (route, error) {
	c, err := candidateOf(ev)
	if err != nil {
		return route{}, fmt.Errorf("%w: %w", queue.ErrGiveUp, err)
	}

	rt, ok := pick(*r.routes.Load(), c)
	if !ok {
		return route{}, fmt.Errorf("%w: %w", queue.ErrGiveUp, errNoRoute)
	}

	return rt, nil
}

// route routes ev to rt in one transaction: it marks the event routed,
// hands it to the grouper, and starts the investigation of a group it
// opened. It reports false, and writes nothing, when the event is no
// longer received.
func (r *Router) route(ctx context.Context, ev db.Event, rt route) (res grouper.Result, routed bool, err error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return grouper.Result{}, false, fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	marked, err := db.New(tx).MarkEventRouted(ctx, db.MarkEventRoutedParams{ID: ev.ID, Route: &rt.name})
	if err != nil {
		return grouper.Result{}, false, fmt.Errorf("mark the event routed: %w", err)
	}

	if marked == 0 {
		return grouper.Result{}, false, nil
	}

	res, err = r.grouper.Group(ctx, tx, ev, rt.grouping)
	if err != nil {
		return grouper.Result{}, false, fmt.Errorf("group the event: %w", err)
	}

	if res.Opened {
		if err := startInvestigation(ctx, tx, ev, rt, res); err != nil {
			return grouper.Result{}, false, fmt.Errorf("investigation %s: %w", res.InvestigationID, err)
		}

		if err := r.startWatchers(ctx, tx, ev, rt, res); err != nil {
			return grouper.Result{}, false, fmt.Errorf("group %s: %w", res.GroupID, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return grouper.Result{}, false, fmt.Errorf("commit: %w", err)
	}

	return res, true, nil
}

// startInvestigation creates the queued investigation of the group the
// grouper opened for ev, enqueues its investigate job and opens the
// route's outputs, so each shows "Looking into it" at once.
func startInvestigation(ctx context.Context, tx pgx.Tx, ev db.Event, rt route, res grouper.Result) error {
	if err := db.New(tx).InsertInvestigation(ctx, db.InsertInvestigationParams{
		ID:              res.InvestigationID,
		GroupID:         res.GroupID,
		Route:           rt.name,
		Strategy:        &rt.strategy,
		StrategyVersion: &rt.strategyVersion,
		Mode:            string(action.EffectiveMode(rt.mode, trust(ev), false)),
		Status:          investigator.StatusQueued,
		Connectors:      rt.connectors,
		Budget:          rt.budget,
	}); err != nil {
		return fmt.Errorf("create the investigation: %w", err)
	}

	if err := enqueueInvestigate(ctx, tx, rt.name, res); err != nil {
		return fmt.Errorf("enqueue its job: %w", err)
	}

	if err := publish.Open(ctx, tx, res.InvestigationID, rt.outputs); err != nil {
		return fmt.Errorf("open its outputs: %w", err)
	}

	return nil
}

// startWatchers starts the watchers of rt and of its strategy that match
// ev, for the group the grouper opened for it, in tx. A watcher over
// watch.maxPerGroup or watch.maxTotal is not started: a line is logged,
// and the investigation's inbox says so and why.
func (r *Router) startWatchers(ctx context.Context, tx pgx.Tx, ev db.Event, rt route, res grouper.Result) error {
	if len(rt.watchers) == 0 {
		return nil
	}

	c, err := candidateOf(ev)
	if err != nil {
		return fmt.Errorf("read the event: %w", err)
	}

	for _, d := range rt.watchers {
		if !d.Match.Matches(deref(ev.AlertName), c.text) {
			continue
		}

		_, err := watch.Start(ctx, tx, r.watch, watch.NewWatcher{
			GroupID:         res.GroupID,
			InvestigationID: res.InvestigationID,
			Name:            d.Name,
			Origin:          d.Origin,
			Check:           d.Check,
			Settings:        d.Settings,
		}, r.now(), r.watchers)

		if limit, ok := errors.AsType[*watch.LimitError](err); ok {
			r.log.WarnContext(ctx, "watcher not started: a limit is reached",
				"group_id", res.GroupID.String(), "watcher", d.Name, "limit", limit.Limit)

			if err := watch.NotStarted(ctx, tx, res.InvestigationID, d.Name, d.Check.Type(), limit.Error()); err != nil {
				return fmt.Errorf("watcher %s: %w", d.Name, err)
			}

			continue
		}

		if err != nil && !errors.Is(err, watch.ErrNameTaken) {
			return fmt.Errorf("start watcher %s: %w", d.Name, err)
		}
	}

	return nil
}

// enqueueInvestigate adds the investigate job of the group in res. A job
// that is already pending or running for the group counts as added.
func enqueueInvestigate(ctx context.Context, tx pgx.Tx, routeName string, res grouper.Result) error {
	payload, err := json.Marshal(investigator.Payload{InvestigationID: res.InvestigationID, GroupID: res.GroupID})
	if err != nil {
		return fmt.Errorf("encode the investigate payload: %w", err)
	}

	_, err = queue.Enqueue(ctx, tx, queue.NewJob{
		Kind:      queue.KindInvestigate,
		Route:     routeName,
		Payload:   payload,
		UniqueKey: investigator.UniqueKey(res.GroupID),
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

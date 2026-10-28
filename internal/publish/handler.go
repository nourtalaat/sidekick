package publish

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

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/feedback"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// SpanName is the name of the span of one publish job.
const SpanName = "output.publish"

// tracerName names the tracer of this package.
const tracerName = "github.com/nourtalaat/sidekick/internal/publish"

// Span attributes.
const (
	attrSink  = "sidekick.sink"
	attrPhase = "sidekick.phase"
)

var (
	errNoInvestigation = errors.New("the payload has no investigation_id")
	errUnknownSink     = errors.New("no output with this name is configured")
	errNoState         = errors.New("the investigation has no publish state")
)

// Options set up a Handler. Pool is required.
type Options struct {
	// Pool is the database the investigations, publish states, output refs
	// and jobs are in.
	Pool *pgxpool.Pool
	// Sinks are the built outputs, by name.
	Sinks map[string]output.Sink
	// Logger receives a line per publish, with IDs only. Nil logs nothing.
	Logger *slog.Logger
	// TracerProvider makes the tracer of the output.publish spans. Nil
	// means the global tracer provider.
	TracerProvider trace.TracerProvider
	// Watchers returns the state of every watcher of a group, for the live
	// display in every update. Nil shows no watchers.
	Watchers func(ctx context.Context, q db.DBTX, groupID uuid.UUID) ([]output.WatcherState, error)
}

// Handler runs publish jobs. Make it with New and register it for
// queue.KindPublish.
type Handler struct {
	pool     *pgxpool.Pool
	sinks    map[string]output.Sink
	log      *slog.Logger
	tracer   trace.Tracer
	watchers func(ctx context.Context, q db.DBTX, groupID uuid.UUID) ([]output.WatcherState, error)
}

var _ queue.Handler = (*Handler)(nil)

// New returns a Handler for the sinks in opts.
func New(opts Options) *Handler {
	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	return &Handler{pool: opts.Pool, sinks: opts.Sinks, log: log, tracer: tp.Tracer(tracerName), watchers: opts.Watchers}
}

// Handle shows the newest state of one investigation in one sink, inside
// an output.publish span that is a child of the span that enqueued the
// job. It first sends the watcher changes the sink has not been sent, when
// the sink accepts them, then publishes the state when it is newer than
// what the sink shows and the sink accepts its phase, and finishes the run
// as the package doc describes.
//
// A failed publish returns the error, so the job is retried with backoff
// and keeps its unique key meanwhile. A payload that does not decode, a
// sink that is no longer configured, or an investigation without a publish
// state gives up the job.
func (h *Handler) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := h.tracer.Start(queue.TraceContext(ctx, job), SpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "publish failed")
		}

		span.End()
	}()

	p, err := decodePayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the publish payload: %w: %w", queue.ErrGiveUp, err)
	}

	span.SetAttributes(attribute.String(attrSink, p.Sink))

	sink, ok := h.sinks[p.Sink]
	if !ok {
		return fmt.Errorf("sink %q: %w: %w", p.Sink, queue.ErrGiveUp, errUnknownSink)
	}

	state, err := db.New(h.pool).GetPublishState(ctx, db.GetPublishStateParams{InvestigationID: p.InvestigationID, Sink: p.Sink})
	if errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("investigation %s: %w: %w", p.InvestigationID, queue.ErrGiveUp, errNoState)
	}

	if err != nil {
		return fmt.Errorf("load the publish state of %s: %w", p.InvestigationID, err)
	}

	span.SetAttributes(attribute.String(attrPhase, state.Phase))

	log := h.log.With("investigation_id", p.InvestigationID.String(), "sink", p.Sink, "job_id", job.ID, "phase", state.Phase, "seq", state.Seq)

	ref, err := h.publish(ctx, p, sink, state)
	if err != nil {
		return fmt.Errorf("publish investigation %s to %s: %w", p.InvestigationID, p.Sink, err)
	}

	if ref != nil {
		log.DebugContext(ctx, "published", "external", ref.External)
	}

	if err := h.finish(ctx, job, p, state.Seq, ref); err != nil {
		return fmt.Errorf("finish publishing investigation %s to %s: %w", p.InvestigationID, p.Sink, err)
	}

	return nil
}

// publish sends sink the watcher changes it has not been sent, then state,
// and returns the ref the sink returned for state. It returns nil, and
// sends no state, when the sink shows this state already or does not
// accept its phase.
func (h *Handler) publish(ctx context.Context, p Payload, sink output.Sink, state db.GetPublishStateRow) (*output.Ref, error) {
	phase := output.Phase(state.Phase)
	shown := state.PreviousSeq

	var changes []db.ListUnsentWatcherChangesRow

	if sink.Accepts(output.PhaseWatcher) {
		var err error

		changes, err = db.New(h.pool).ListUnsentWatcherChanges(ctx, db.ListUnsentWatcherChangesParams{
			InvestigationID: p.InvestigationID,
			MaxSeq:          state.Seq,
			Sink:            p.Sink,
		})
		if err != nil {
			return nil, fmt.Errorf("list the watcher changes to send: %w", err)
		}
	}

	showState := (shown == nil || state.Seq > *shown) && sink.Accepts(phase)
	if len(changes) == 0 && !showState {
		return nil, nil
	}

	u, err := h.update(ctx, p.InvestigationID, state)
	if err != nil {
		return nil, fmt.Errorf("build the update: %w", err)
	}

	u.Previous, shown, err = h.sendChanges(ctx, p, sink, u, changes, shown)
	if err != nil {
		return nil, fmt.Errorf("watcher changes: %w", err)
	}

	if !showState || (shown != nil && state.Seq <= *shown) {
		return nil, nil
	}

	ref, err := sink.Publish(ctx, u)
	if err != nil {
		return nil, fmt.Errorf("sink: %w", err)
	}

	return &ref, nil
}

// sendChanges sends sink the watcher changes, oldest first, each as an
// output.PhaseWatcher update built on u with the change's seq. After each
// one it stores the ref the sink returned and marks the change sent, so a
// retry sends only the rest. It returns the newest ref and the seq it
// shows, or u.Previous and shown when there are no changes.
func (h *Handler) sendChanges(ctx context.Context, p Payload, sink output.Sink, u output.Update, changes []db.ListUnsentWatcherChangesRow, shown *int64) (*output.Ref, *int64, error) {
	prev := u.Previous

	for _, c := range changes {
		var state output.WatcherState
		if err := json.Unmarshal(c.State, &state); err != nil {
			return nil, nil, fmt.Errorf("decode watcher change %d: %w", c.ID, err)
		}

		cu := u
		cu.Phase = output.PhaseWatcher
		cu.Watcher = &state
		cu.Seq = c.Seq
		cu.Previous = prev

		ref, err := sink.Publish(ctx, cu)
		if err != nil {
			return nil, nil, fmt.Errorf("send watcher change %d: %w", c.ID, err)
		}

		if err := h.markSent(ctx, p, c, ref); err != nil {
			return nil, nil, fmt.Errorf("watcher change %d: %w", c.ID, err)
		}

		prev, shown = &ref, &c.Seq
	}

	return prev, shown, nil
}

// markSent stores the ref a sink returned for watcher change c and marks
// the change sent to the sink, in one transaction.
func (h *Handler) markSent(ctx context.Context, p Payload, c db.ListUnsentWatcherChangesRow, ref output.Ref) error {
	raw, err := json.Marshal(ref)
	if err != nil {
		return fmt.Errorf("encode the ref: %w", err)
	}

	err = pgx.BeginFunc(ctx, h.pool, func(tx pgx.Tx) error {
		queries := db.New(tx)

		if err := queries.SaveOutputRef(ctx, db.SaveOutputRefParams{
			InvestigationID: p.InvestigationID,
			Sink:            p.Sink,
			External:        ref.External,
			Ref:             raw,
			LastSeq:         c.Seq,
		}); err != nil {
			return fmt.Errorf("store the ref: %w", err)
		}

		if err := queries.MarkWatcherChangeSent(ctx, db.MarkWatcherChangeSentParams{Sink: p.Sink, ID: c.ID}); err != nil {
			return fmt.Errorf("mark it sent: %w", err)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("record the watcher change as sent: %w", err)
	}

	return nil
}

// update builds what the sink gets: the state, the event that opened the
// investigation's group, the state of the group's watchers, and the ref of
// what the sink published before.
func (h *Handler) update(ctx context.Context, investigationID uuid.UUID, state db.GetPublishStateRow) (output.Update, error) {
	inv, ev, err := h.load(ctx, investigationID)
	if err != nil {
		return output.Update{}, fmt.Errorf("load the investigation: %w", err)
	}

	var stored body
	if err := json.Unmarshal(state.Body, &stored); err != nil {
		return output.Update{}, fmt.Errorf("decode the publish state: %w", err)
	}

	u := output.Update{
		InvestigationID: investigationID.String(),
		Event:           ev,
		Events:          int(state.EventCount),
		Phase:           output.Phase(state.Phase),
		Report:          stored.Report,
		Seq:             state.Seq,
	}

	for _, line := range stored.Progress {
		u.Progress = append(u.Progress, output.Progress(line))
	}

	if h.watchers != nil {
		u.Watchers, err = h.watchers(ctx, h.pool, state.GroupID)
		if err != nil {
			return output.Update{}, fmt.Errorf("load the watchers of group %s: %w", state.GroupID, err)
		}
	}

	if u.Phase == output.PhaseFinal {
		if err := h.addResponders(ctx, &u, inv); err != nil {
			return output.Update{}, fmt.Errorf("load what responders did: %w", err)
		}
	}

	if state.PreviousRef != nil {
		var prev output.Ref
		if err := json.Unmarshal(state.PreviousRef, &prev); err != nil {
			return output.Update{}, fmt.Errorf("decode the previous ref: %w", err)
		}

		u.Previous = &prev
	}

	return u, nil
}

// load returns the investigation with the event that opened its group.
// The retention job deletes events sooner than investigations by default,
// so an old investigation can have no events left; it is then published
// with an empty event, and the sinks fall back to their wording for an
// event without a title, while the report comes from the publish state.
func (h *Handler) load(ctx context.Context, id uuid.UUID) (db.Investigation, event.Event, error) {
	queries := db.New(h.pool)

	loaded, err := queries.GetInvestigationWithEvent(ctx, id)
	if err == nil {
		ev, err := store.EventOf(loaded.Event)
		if err != nil {
			return db.Investigation{}, event.Event{}, fmt.Errorf("read event %s: %w", loaded.Event.ID, err)
		}

		return loaded.Investigation, ev, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Investigation{}, event.Event{}, fmt.Errorf("with its event: %w", err)
	}

	inv, err := queries.GetInvestigation(ctx, id)
	if err != nil {
		return db.Investigation{}, event.Event{}, fmt.Errorf("without its event: %w", err)
	}

	h.log.DebugContext(ctx, "publishing an investigation whose events were deleted", "investigation_id", id.String())

	return inv, event.Event{}, nil
}

// addResponders adds to a final update who rated the verdict and who
// stopped the run.
func (h *Handler) addResponders(ctx context.Context, u *output.Update, inv db.Investigation) error {
	ratings, err := feedback.Verdicts(ctx, h.pool, inv)
	if err != nil {
		return fmt.Errorf("load the ratings: %w", err)
	}

	u.Feedback = ratings

	stoppedBy, err := stoppedByOf(ctx, h.pool, inv)
	if err != nil {
		return fmt.Errorf("load who stopped the run: %w", err)
	}

	u.StoppedBy = stoppedBy

	return nil
}

// stoppedByOf returns who asked to stop the run of inv, or nil when
// nobody did. A web user or API token is named by the users or api_tokens
// row; one that was deleted since is shown without a name.
func stoppedByOf(ctx context.Context, q db.DBTX, inv db.Investigation) (*output.Person, error) {
	if inv.CancelRequestedBy == nil && inv.CancelRequestedBySlackUser == nil {
		return nil, nil
	}

	p := &output.Person{}

	if inv.CancelRequestedBySlackUser != nil {
		p.SlackUserID = *inv.CancelRequestedBySlackUser
	}

	if inv.CancelRequestedBy != nil {
		p.UserID = *inv.CancelRequestedBy

		actor, err := db.New(q).GetActorName(ctx, *inv.CancelRequestedBy)
		if err != nil {
			return nil, fmt.Errorf("read the name of %s: %w", p.UserID, err)
		}

		p.Name = actor.Name
	}

	return p, nil
}

// finish ends the run in one transaction: it locks the state row, stores
// ref as showing seq when it is set, gives up the job's unique key, and
// enqueues a new job when the state is newer than seq, because an update
// written during the run found the key taken. Taking the state row before
// the job row matches Enqueue.
func (h *Handler) finish(ctx context.Context, job db.Job, p Payload, seq int64, ref *output.Ref) (err error) {
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}

	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			err = errors.Join(err, fmt.Errorf("roll back: %w", rbErr))
		}
	}()

	queries := db.New(tx)

	newest, err := queries.LockPublishSeq(ctx, p.InvestigationID)
	if err != nil {
		return fmt.Errorf("lock the publish state: %w", err)
	}

	if ref != nil {
		raw, err := json.Marshal(ref)
		if err != nil {
			return fmt.Errorf("encode the ref: %w", err)
		}

		if err := queries.SaveOutputRef(ctx, db.SaveOutputRefParams{
			InvestigationID: p.InvestigationID,
			Sink:            p.Sink,
			External:        ref.External,
			Ref:             raw,
			LastSeq:         seq,
		}); err != nil {
			return fmt.Errorf("store the ref: %w", err)
		}
	}

	if err := queue.ReleaseUniqueKey(ctx, tx, job); err != nil {
		return fmt.Errorf("give up the unique key: %w", err)
	}

	if newest > seq {
		if err := enqueueJob(ctx, tx, p); err != nil {
			return fmt.Errorf("enqueue the next publish: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	return nil
}

// decodePayload reads a publish payload.
func decodePayload(raw []byte) (Payload, error) {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Payload{}, fmt.Errorf("decode: %w", err)
	}

	if p.InvestigationID == uuid.Nil {
		return Payload{}, errNoInvestigation
	}

	if p.Sink == "" {
		return Payload{}, errUnknownSink
	}

	return p, nil
}

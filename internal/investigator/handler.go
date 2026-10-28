package investigator

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
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/memory"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// SpanName is the name of the span of one run of an investigate job.
const SpanName = "investigation.run"

// tracerName names the tracer of this package.
const tracerName = "github.com/nourtalaat/sidekick/internal/investigator"

// Span attributes.
const (
	attrInvestigationID = "sidekick.investigation.id"
	attrRoute           = "sidekick.route"
	attrStrategy        = "sidekick.strategy"
	attrRun             = "sidekick.run"
)

var (
	errNoInvestigationID = errors.New("the payload has no investigation_id")
	errNotFound          = errors.New("the investigation does not exist")
)

// Options set up a Handler. Pool and Guard are required.
type Options struct {
	// Pool is the database the investigations, steps, publish states and
	// jobs are in.
	Pool *pgxpool.Pool
	// Guard runs the source tool calls.
	Guard *tools.Guard
	// Connectors are the model connectors by name, as investigations.
	// connectors names them. Wrap each with instrument.Wrap first.
	Connectors map[string]Connector
	// Prices price each model step.
	Prices pricing.Table
	// Strategies are the strategies routes name.
	Strategies *strategy.Set
	// Routes are the settings of each route, by name.
	Routes map[string]Route
	// Rules are the checks a submitted report must pass.
	Rules report.Rules
	// Daily are the daily spending caps and their time zone.
	Daily DailyCaps
	// MemoryExamples is the most similar confirmed investigations the
	// prompt shows. Zero shows none.
	MemoryExamples int
	// MemoryVectors adds the entries nearest in meaning to the examples,
	// with vector memory on. Nil means full-text matches only.
	MemoryVectors *memory.Vectors
	// Watchers starts the watchers the model asks for with start_watcher.
	// Nil offers the tool to no investigation.
	Watchers *watch.ModelStarter
	// Logger receives a line when a run starts and ends, with IDs, the
	// route, the verdict and the stop reason only, never event text, tool
	// results or model output. Nil logs nothing.
	Logger *slog.Logger
	// Metrics records the investigation metrics, and New registers the
	// gauge of each route's spend today with them. Nil records none.
	Metrics *Metrics
	// Canceller stops the model or tool call in flight when a responder
	// stops a run. Nil means a run sees a stop only at its next step.
	Canceller *Canceller
	// TracerProvider makes the tracer of the investigation.run spans. Nil
	// means the global tracer provider.
	TracerProvider trace.TracerProvider
}

// Connector is one model connector with its retry setting.
type Connector struct {
	Connector model.Connector
	// Type is the connector's provider type, such as anthropic, which the
	// model.Reasoning parts it returns name as their Provider. Reasoning
	// from another type is not sent to it. Empty sends all reasoning.
	Type string
	// Retries is how many times a call that failed with a retryable error
	// is repeated.
	Retries int
}

// Route is what an investigation run needs to know about its route.
type Route struct {
	// Sources are the sources the investigation may use; nil means every
	// source.
	Sources []string
	// Outputs are the route's outputs, which get its daily cap notice.
	Outputs []string
	// DailyMaxCostMicros is the route's daily cap. Zero means only the
	// global cap holds it.
	DailyMaxCostMicros int64
	// DailySkip ends the route's new investigations at its daily cap
	// instead of holding them until the next day.
	DailySkip bool
}

// Handler runs investigate jobs. Make it with New, register it for
// queue.KindInvestigate, and set OnDead as the kind's OnDead hook.
type Handler struct {
	opts     Options
	builtins builtins
	triager  triager
	log      *slog.Logger
	tracer   trace.Tracer
	// now is the clock of the soft limits. Tests replace it.
	now func() time.Time
}

var _ queue.Handler = (*Handler)(nil)

// New returns a Handler.
func New(opts Options) (*Handler, error) {
	if opts.Pool == nil || opts.Guard == nil {
		return nil, errors.New("new investigator: Pool and Guard are required")
	}

	submit, err := tools.ParseInputSchema(report.SubmitVerdictSchema)
	if err != nil {
		return nil, fmt.Errorf("new investigator: %s: %w", report.SubmitVerdictTool, err)
	}

	record, err := tools.ParseInputSchema(recordFindingSchema)
	if err != nil {
		return nil, fmt.Errorf("new investigator: %s: %w", RecordFindingTool, err)
	}

	answer, err := tools.ParseInputSchema(report.AnswerFollowUpSchema)
	if err != nil {
		return nil, fmt.Errorf("new investigator: %s: %w", report.AnswerFollowUpTool, err)
	}

	triage, err := tools.ParseInputSchema(submitTriageSchema)
	if err != nil {
		return nil, fmt.Errorf("new investigator: %s: %w", prompt.SubmitTriageTool, err)
	}

	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	h := &Handler{
		opts:     opts,
		builtins: builtins{submitVerdict: submit, recordFinding: record, answerFollowUp: answer},
		triager:  triager{schema: triage, strategies: opts.Strategies},
		log:      log,
		tracer:   tp.Tracer(tracerName),
		now:      time.Now,
	}

	if err := opts.Metrics.register(newSpendCollector(h)); err != nil {
		return nil, fmt.Errorf("new investigator: %w", err)
	}

	return h, nil
}

// Handle runs one investigate job inside an investigation.run span, a
// child of the span that enqueued the job. It takes over the run with the
// job's claim token as the fence, then runs the agent loop from the last
// saved step until the investigation ends. Before an investigation's
// first run, a daily cap that is reached holds it until the next day or
// ends it unrun, as the cap says, and posts the cap's notice.
//
// It returns nil when the investigation ended, or had ended already. A
// payload that does not decode, an investigation that does not exist, or
// a route, connector or strategy that is no longer configured gives up the
// job, so OnDead marks the investigation failed. A worker that finds a
// newer claim owning the run returns an error that wraps
// queue.ErrLeaseLost. Other errors retry the job, which resumes from the
// last saved step.
func (h *Handler) Handle(ctx context.Context, job db.Job) (err error) {
	ctx, span := h.tracer.Start(queue.TraceContext(ctx, job), SpanName)

	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, "the investigation run failed")
		}

		span.End()
	}()

	id, err := decodePayload(job.Payload)
	if err != nil {
		return fmt.Errorf("decode the investigate payload: %w: %w", queue.ErrGiveUp, err)
	}

	span.SetAttributes(attribute.String(attrInvestigationID, id.String()))

	held, skip, err := h.holdForDailyCap(ctx, job, id)
	if err != nil {
		return fmt.Errorf("check the daily caps: %w", err)
	}

	if held {
		return nil
	}

	inv, started, err := h.start(ctx, id, queue.ClaimToken(job))
	if err != nil {
		return fmt.Errorf("take over the run: %w", err)
	}

	if !started {
		return nil
	}

	span.SetAttributes(
		attribute.String(attrRoute, inv.Route),
		attribute.String(attrStrategy, deref(inv.Strategy)),
		attribute.Int(attrRun, int(inv.Run)),
	)

	log := h.log.With("investigation_id", id.String(), "job_id", job.ID, "route", inv.Route)

	if skip != nil {
		return h.skipForDailyCap(ctx, inv, *skip, log)
	}

	done := h.opts.Metrics.running(inv.Route)
	defer done()

	r, err := h.newRun(ctx, inv, log)
	if err != nil {
		return fmt.Errorf("investigation %s: %w", id, err)
	}

	if err := r.loop(ctx); err != nil {
		return fmt.Errorf("investigation %s: %w", id, err)
	}

	return nil
}

// start gives the run of the investigation to the claim with token. It
// returns false, and no error, for an investigation that has ended.
func (h *Handler) start(ctx context.Context, id uuid.UUID, token int64) (db.Investigation, bool, error) {
	queries := db.New(h.opts.Pool)

	inv, err := queries.StartInvestigationRun(ctx, db.StartInvestigationRunParams{ID: id, Fence: token, Now: h.now()})
	if err == nil {
		return inv, true, nil
	}

	if !errors.Is(err, pgx.ErrNoRows) {
		return db.Investigation{}, false, fmt.Errorf("start investigation %s: %w", id, err)
	}

	inv, err = queries.GetInvestigation(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.Investigation{}, false, fmt.Errorf("investigation %s: %w: %w", id, queue.ErrGiveUp, errNotFound)
	}

	if err != nil {
		return db.Investigation{}, false, fmt.Errorf("load investigation %s: %w", id, err)
	}

	if inv.Fence >= token {
		return db.Investigation{}, false, fmt.Errorf("investigation %s: a newer claim owns the run: %w", id, queue.ErrLeaseLost)
	}

	h.log.DebugContext(ctx, "investigation not run: it has ended", "investigation_id", id.String(), "status", inv.Status)

	return db.Investigation{}, false, nil
}

// decodePayload returns the investigation ID of an investigate payload.
func decodePayload(raw []byte) (uuid.UUID, error) {
	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return uuid.Nil, fmt.Errorf("decode: %w", err)
	}

	if p.InvestigationID == uuid.Nil {
		return uuid.Nil, errNoInvestigationID
	}

	return p.InvestigationID, nil
}

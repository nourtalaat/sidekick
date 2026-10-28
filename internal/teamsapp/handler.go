package teamsapp

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/intake/ingest"
	"github.com/nourtalaat/sidekick/internal/listen"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/teams"
)

// AckBudget is how long handling one activity may take. The Bot Framework
// waits 15 seconds for the answer; an activity that takes longer is
// answered with an error.
const AckBudget = 10 * time.Second

// SpanName is the name of the span that stores one Teams event. It is the
// same span webhook deliveries and Slack events have.
const SpanName = "intake.receive"

// tracerName names the tracer of this package.
const tracerName = "github.com/nourtalaat/sidekick/internal/teamsapp"

// Span attributes, as on webhook deliveries.
const (
	attrIntake    = "sidekick.intake"
	attrEventKey  = "sidekick.event.key"
	attrDuplicate = "sidekick.duplicate"
)

// errNoTeams is returned by New for a config without a teams section.
var errNoTeams = errors.New("the teams section is required")

// Options set up a Handler. Config, Secrets, Redactor, Pool and Logger are
// required.
type Options struct {
	// Config holds the teams section and the listeners.
	Config *config.Config
	// Secrets resolves the client secret on every token request, so a
	// rotated secret is used without a restart.
	Secrets registry.Secrets
	// Redactor redacts events before they are stored.
	Redactor *redact.Redactor
	// Pool is the database events are stored in.
	Pool *pgxpool.Pool
	// HTTP calls Microsoft. Nil means httpclient.New().
	HTTP *http.Client
	// Logger receives a line per stored event. Message text is never
	// logged.
	Logger *slog.Logger
	// Metrics is where the intake metrics are registered. Nil records
	// nothing.
	Metrics prometheus.Registerer
	// TracerProvider makes the tracer of the intake.receive spans. Nil
	// means the global tracer provider.
	TracerProvider trace.TracerProvider
	// LoginURL, MetadataURL and GraphURL replace teams.DefaultLoginURL,
	// teams.DefaultOpenIDMetadataURL and DefaultGraphURL when set, for
	// tests.
	LoginURL    string
	MetadataURL string
	GraphURL    string
}

// Handler turns Teams activities into stored events. It is safe for
// concurrent use.
type Handler struct {
	listeners   []listener
	appID       string
	tenantID    string
	trustedBots []string
	verifier    *teams.Verifier
	graph       *graph
	writer      *ingest.Writer
	pool        *pgxpool.Pool
	metrics     *ingest.Metrics
	log         *slog.Logger
	tracer      trace.Tracer
	now         func() time.Time
	// ackBudget is AckBudget; tests on a slow database raise it.
	ackBudget time.Duration
}

// handle stores the event of one verified activity from the bot's
// tenant, if it is one Sidekick acts on. It returns nil when the Bot
// Framework may be answered with 200: the event is committed, was stored
// before, or is one Sidekick ignores. Any error means the activity should
// be answered with an error.
func (h *Handler) handle(ctx context.Context, a activity, raw json.RawMessage) error {
	ctx, cancel := context.WithTimeout(ctx, h.ackBudget)
	defer cancel()

	if a.Type != activityMessage || !a.inChannel() || a.ID == "" || a.fromRecipient() {
		return nil
	}

	if a.mentionsRecipient() {
		return h.handleMention(ctx, a, raw)
	}

	if a.isThreadReply() {
		return nil
	}

	text := a.flatText()

	l := h.firstListener(ctx, a, text)
	if l == nil {
		return nil
	}

	ev := buildEvent(eventKey(a.channel(), a.ID), a, text, l, raw)
	trusted := l.Trusted && a.isBot() && slices.ContainsFunc(h.trustedBots, func(id string) bool { return strings.EqualFold(id, a.From.ID) })

	return h.store(ctx, l.Name, trusted, ev)
}

// handleMention stores a mention of the bot. In a thread it is for the
// thread's first message: the event a listener already stored for it, or
// else the message read through Graph, with the fields and fingerprint a
// listener on it gives it. At the top level, or when Graph may not read
// the first message, it is for the mention's own text. Mentions are never
// trusted.
func (h *Handler) handleMention(ctx context.Context, a activity, raw json.RawMessage) error {
	key := eventKey(a.channel(), a.ID)
	ev := buildEvent(key, a, a.flatText(), nil, raw)

	if a.isThreadReply() {
		parent, found, err := h.threadParent(ctx, a, key, raw)

		switch {
		case errors.Is(err, errMessageUnreadable):
			h.log.WarnContext(ctx, "cannot read the first message of the Teams thread; investigating the mention's own text. Does the team grant the app ChannelMessage.Read.Group?", "error", err.Error())
		case err != nil:
			return fmt.Errorf("mention in a thread: %w", err)
		case !found:
			return nil
		default:
			ev = parent
		}
	}

	ev.Kind = event.KindMention

	return h.store(ctx, config.TeamsMentionIntake, false, ev)
}

// threadParent returns the event for the first message of a's thread,
// with key: from the event a listener stored for it, else from the
// message as Graph returns it. found is false when the first message is
// the bot's own, which leaves the mention to follow-ups.
func (h *Handler) threadParent(ctx context.Context, a activity, key string, raw json.RawMessage) (ev event.Event, found bool, err error) {
	channel, root := a.channel(), a.thread()

	stored, ok, err := h.storedEvent(ctx, eventKey(channel, root))
	if err != nil {
		return event.Event{}, false, fmt.Errorf("look up the stored first message: %w", err)
	}

	if ok {
		stored.Key, stored.Raw = key, raw

		return stored, true, nil
	}

	team := a.team()
	if team == "" {
		return event.Event{}, false, fmt.Errorf("the activity names no team: %w", errMessageUnreadable)
	}

	m, err := h.graph.message(ctx, team, channel, root)
	if err != nil {
		return event.Event{}, false, fmt.Errorf("read the first message: %w", err)
	}

	parent := activityOf(m, a)
	if strings.EqualFold(parent.From.ID, a.Recipient.ID) {
		return event.Event{}, false, nil
	}

	text := parent.flatText()

	return buildEvent(key, parent, text, h.firstListener(ctx, parent, text), raw), true, nil
}

// storedEvent returns the event a Teams listener stored with key, if any.
func (h *Handler) storedEvent(ctx context.Context, key string) (event.Event, bool, error) {
	if len(h.listeners) == 0 {
		return event.Event{}, false, nil
	}

	names := make([]string, 0, len(h.listeners))
	for _, l := range h.listeners {
		names = append(names, l.Name)
	}

	row, err := db.New(h.pool).GetEventByKey(ctx, db.GetEventByKeyParams{Intakes: names, Key: h.writer.StoredKey(key)})
	if errors.Is(err, pgx.ErrNoRows) {
		return event.Event{}, false, nil
	}

	if err != nil {
		return event.Event{}, false, fmt.Errorf("get the event: %w", err)
	}

	ev, err := store.EventOf(row)
	if err != nil {
		return event.Event{}, false, fmt.Errorf("read the event: %w", err)
	}

	return ev, true, nil
}

// firstListener returns the first listener, in name order, that matches
// a, or nil. The other listeners that match too are logged at debug.
func (h *Handler) firstListener(ctx context.Context, a activity, text string) *listener {
	var matched []*listener

	for i := range h.listeners {
		if l := &h.listeners[i]; l.matches(a, text) {
			matched = append(matched, l)
		}
	}

	if len(matched) == 0 {
		return nil
	}

	if len(matched) > 1 {
		others := make([]string, 0, len(matched)-1)
		for _, l := range matched[1:] {
			others = append(others, l.Name)
		}

		h.log.DebugContext(ctx, "Teams message matches more than one listener; the first one takes it", "listener", matched[0].Name, "also_matched", others)
	}

	return matched[0]
}

// store stores ev for intake in an intake.receive span, and counts it.
func (h *Handler) store(ctx context.Context, intake string, trusted bool, ev event.Event) error {
	ctx, span := h.tracer.Start(ctx, SpanName, trace.WithAttributes(attribute.String(attrIntake, intake)))
	defer span.End()

	stored, err := h.writer.Store(ctx, ingest.Delivery{Intake: intake, Trusted: trusted, ReceivedAt: h.now(), Events: []event.Event{ev}})
	if err != nil {
		span.SetStatus(codes.Error, "Teams event not stored")

		return fmt.Errorf("store the Teams event: %w", err)
	}

	h.metrics.Stored(intake, stored)

	s := stored[0]
	span.SetAttributes(attribute.StringSlice(attrEventKey, []string{s.Key}), attribute.Bool(attrDuplicate, s.Duplicate))

	log := h.log.With("intake", intake, "kind", string(ev.Kind), "duplicate", s.Duplicate)
	if !s.Duplicate {
		log = log.With("event_id", s.ID.String())
	}

	log.InfoContext(ctx, "Teams event stored")

	return nil
}

// New builds a Handler from the teams section and the Teams listeners of
// opts.Config.
func New(opts Options) (*Handler, error) {
	cfg := opts.Config.Teams
	if cfg == nil {
		return nil, errNoTeams
	}

	rules, err := listen.CompileAll(opts.Config.Listeners, config.ListenerSourceTeams)
	if err != nil {
		return nil, fmt.Errorf("compile the listeners: %w", err)
	}

	hc := cmp.Or(opts.HTTP, httpclient.New())

	verifier, err := teams.NewVerifier(teams.VerifierOptions{AppID: cfg.AppID, MetadataURL: opts.MetadataURL, HTTP: hc, Logger: opts.Logger})
	if err != nil {
		return nil, fmt.Errorf("set up the token check: %w", err)
	}

	graphTokens, err := teams.NewTokens(teams.TokenOptions{
		Credentials: teams.Credentials{AppID: cfg.AppID, TenantID: cfg.TenantID, ClientSecret: cfg.ClientSecret, WorkloadIdentity: cfg.WorkloadIdentity},
		Scope:       teams.GraphScope,
		Secrets:     opts.Secrets,
		HTTP:        hc,
		LoginURL:    opts.LoginURL,
	})
	if err != nil {
		return nil, fmt.Errorf("set up Graph tokens: %w", err)
	}

	graphURL := cmp.Or(opts.GraphURL, DefaultGraphURL)
	if err := teams.CheckBaseURL(graphURL); err != nil {
		return nil, fmt.Errorf("graph URL: %w", err)
	}

	metrics, err := ingest.NewMetrics(opts.Metrics)
	if err != nil {
		return nil, fmt.Errorf("set up intake metrics: %w", err)
	}

	metrics.Init(config.TeamsMentionIntake)

	listeners := make([]listener, 0, len(rules))
	for _, r := range rules {
		listeners = append(listeners, listener{Rules: r})
		metrics.Init(r.Name)
	}

	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	return &Handler{
		listeners:   listeners,
		appID:       cfg.AppID,
		tenantID:    cfg.TenantID,
		trustedBots: cfg.TrustedBots,
		verifier:    verifier,
		graph:       &graph{baseURL: graphURL, client: hc, tokens: graphTokens},
		writer:      ingest.NewWriter(opts.Pool, opts.Redactor),
		pool:        opts.Pool,
		metrics:     metrics,
		log:         opts.Logger,
		tracer:      tp.Tracer(tracerName),
		now:         time.Now,
		ackBudget:   AckBudget,
	}, nil
}

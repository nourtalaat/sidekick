package slackapp

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
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// AckBudget is how long handling one payload may take, so Slack is
// acknowledged within its 3 second limit. A payload that takes longer is not
// acknowledged, and Slack sends it again.
const AckBudget = 2500 * time.Millisecond

// SpanName is the name of the span that stores one Slack event. It is the
// same span webhook deliveries have.
const SpanName = "intake.receive"

// tracerName names the tracer of this package.
const tracerName = "github.com/nourtalaat/sidekick/internal/slackapp"

// Span attributes, as on webhook deliveries.
const (
	attrIntake    = "sidekick.intake"
	attrEventKey  = "sidekick.event.key"
	attrDuplicate = "sidekick.duplicate"
)

// ErrInvalidPayload is wrapped by Handle for a payload that is not an
// Events API payload. Sending it again cannot help.
var ErrInvalidPayload = errors.New("invalid Slack payload")

// errNoSlack is returned by New for a config without a slack section.
var errNoSlack = errors.New("the slack section is required")

// Options set up a Handler. Config, Secrets, Redactor, Pool and Logger are
// required.
type Options struct {
	// Config holds the slack section and the listeners.
	Config *config.Config
	// Secrets resolves the bot token on every Web API call, so a rotated
	// token is used without a restart.
	Secrets registry.Secrets
	// Redactor redacts events before they are stored.
	Redactor *redact.Redactor
	// Pool is the database events are stored in.
	Pool *pgxpool.Pool
	// HTTP calls the Slack Web API. Nil means httpclient.New().
	HTTP *http.Client
	// Logger receives a line per stored event. Message text is never
	// logged.
	Logger *slog.Logger
	// Metrics is where the intake and Slack API metrics are registered.
	// Nil records nothing.
	Metrics prometheus.Registerer
	// TracerProvider makes the tracer of the intake.receive spans. Nil
	// means the global tracer provider.
	TracerProvider trace.TracerProvider
}

// Handler turns Events API payloads into stored events. Socket Mode and
// the Events API both use it. It is safe for concurrent use.
type Handler struct {
	listeners     []listener
	trustedBots   []string
	secrets       registry.Secrets
	signingSecret registry.SecretRef
	api           *webAPI
	writer        *ingest.Writer
	metrics       *ingest.Metrics
	log           *slog.Logger
	tracer        trace.Tracer
	now           func() time.Time
	// ackBudget is AckBudget; tests on a slow database raise it.
	ackBudget time.Duration
}

// New builds a Handler from the slack section and the listeners of
// opts.Config.
func New(opts Options) (*Handler, error) {
	cfg := opts.Config.Slack
	if cfg == nil {
		return nil, errNoSlack
	}

	listeners, err := compileListeners(opts.Config.Listeners)
	if err != nil {
		return nil, fmt.Errorf("compile the listeners: %w", err)
	}

	token := func(ctx context.Context) (string, error) {
		return secrets.ResolveText(ctx, opts.Secrets, cfg.BotTokenSecret)
	}

	api, err := newWebAPI(cmp.Or(cfg.APIURL, DefaultAPIURL), cmp.Or(opts.HTTP, httpclient.New()), token, opts.Metrics)
	if err != nil {
		return nil, fmt.Errorf("set up the Slack Web API client: %w", err)
	}

	metrics, err := ingest.NewMetrics(opts.Metrics)
	if err != nil {
		return nil, fmt.Errorf("set up intake metrics: %w", err)
	}

	metrics.Init(config.SlackMentionIntake)

	for _, l := range listeners {
		metrics.Init(l.name)
	}

	tp := opts.TracerProvider
	if tp == nil {
		tp = otel.GetTracerProvider()
	}

	return &Handler{
		listeners:     listeners,
		trustedBots:   cfg.TrustedBots,
		secrets:       opts.Secrets,
		signingSecret: cfg.SigningSecret,
		api:           api,
		writer:        ingest.NewWriter(opts.Pool, opts.Redactor),
		metrics:       metrics,
		log:           opts.Logger,
		tracer:        tp.Tracer(tracerName),
		now:           time.Now,
		ackBudget:     AckBudget,
	}, nil
}

// Handle stores the event in one Events API payload, if it is one Sidekick
// acts on, within AckBudget. It returns nil when Slack may be acknowledged:
// the event is committed, was stored before, or is one Sidekick ignores. An
// error that wraps ErrInvalidPayload means the payload can never be read;
// any other error means Slack should send the payload again.
func (h *Handler) Handle(ctx context.Context, payload []byte) error {
	ctx, cancel := context.WithTimeout(ctx, h.ackBudget)
	defer cancel()

	var env envelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPayload, err)
	}

	if env.Type != payloadEventCallback {
		return nil
	}

	var m message
	if err := json.Unmarshal(env.Event, &m); err != nil {
		return fmt.Errorf("%w: event: %w", ErrInvalidPayload, err)
	}

	if env.EventID == "" {
		return fmt.Errorf("%w: event_id is empty", ErrInvalidPayload)
	}

	switch m.Type {
	case eventMessage:
		return h.handleMessage(ctx, env, m)
	case eventAppMention:
		return h.handleMention(ctx, env, m)
	default:
		return nil
	}
}

// handleMessage stores a channel message that a listener matches. Edits,
// thread replies, Sidekick's own messages and messages that mention
// Sidekick, which arrive as app_mention too, are left out.
func (h *Handler) handleMessage(ctx context.Context, env envelope, m message) error {
	bot := env.botUserID()
	if !m.isPost() || m.isThreadReply() || m.fromApp(env.APIAppID, bot) || m.mentions(bot) {
		return nil
	}

	text := m.flatText()

	l, err := h.firstListener(ctx, m, text)
	if err != nil || l == nil {
		return err
	}

	ev := buildEvent(env.EventID, env.TeamID, m, text, l, env.Event)
	trusted := l.trusted && m.isBot() && slices.Contains(h.trustedBots, m.botID())

	return h.store(ctx, l.name, trusted, ev)
}

// handleMention stores a mention of Sidekick. In a thread it is for the
// thread's first message, read with conversations.replies, with the fields
// and fingerprint a listener on that message gives it; at the top level it
// is for the mention's own text without the mention. Mentions are never
// trusted.
func (h *Handler) handleMention(ctx context.Context, env envelope, m message) error {
	bot := env.botUserID()
	if m.fromApp(env.APIAppID, bot) {
		return nil
	}

	var ev event.Event

	if m.isThreadReply() {
		parent, raw, err := h.api.threadParent(ctx, m.Channel, m.ThreadTS)
		if err != nil {
			return fmt.Errorf("mention in a thread: %w", err)
		}

		if parent.fromApp(env.APIAppID, bot) {
			return nil
		}

		text := parent.flatText()

		l, err := h.firstListener(ctx, parent, text)
		if err != nil {
			return fmt.Errorf("mention in a thread: %w", err)
		}

		ev = buildEvent(env.EventID, env.TeamID, parent, text, l, raw)
	} else {
		text := strings.TrimSpace(strings.ReplaceAll(m.flatText(), mentionOf(bot), ""))
		ev = buildEvent(env.EventID, env.TeamID, m, text, nil, env.Event)
	}

	ev.Kind = event.KindMention

	return h.store(ctx, config.SlackMentionIntake, false, ev)
}

// firstListener returns the first listener, in name order, that matches
// m, or nil. The other listeners that match too are logged at debug. A
// listener that names its channel needs the channel's name, which is
// looked up only when such a listener's other checks pass.
func (h *Handler) firstListener(ctx context.Context, m message, text string) (*listener, error) {
	var (
		matched []*listener
		name    string
		looked  bool
	)

	for i := range h.listeners {
		l := &h.listeners[i]
		if !l.matches(m, text) {
			continue
		}

		if l.byName() && !looked {
			var err error
			if name, err = h.api.channelName(ctx, m.Channel); err != nil {
				return nil, fmt.Errorf("match listeners: %w", err)
			}

			looked = true
		}

		if l.matchesChannel(m.Channel, name) {
			matched = append(matched, l)
		}
	}

	if len(matched) == 0 {
		return nil, nil
	}

	if len(matched) > 1 {
		others := make([]string, 0, len(matched)-1)
		for _, l := range matched[1:] {
			others = append(others, l.name)
		}

		h.log.DebugContext(ctx, "Slack message matches more than one listener; the first one takes it", "listener", matched[0].name, "also_matched", others)
	}

	return matched[0], nil
}

// store stores ev for intake in an intake.receive span, and counts it.
func (h *Handler) store(ctx context.Context, intake string, trusted bool, ev event.Event) error {
	ctx, span := h.tracer.Start(ctx, SpanName, trace.WithAttributes(attribute.String(attrIntake, intake)))
	defer span.End()

	stored, err := h.writer.Store(ctx, ingest.Delivery{Intake: intake, Trusted: trusted, ReceivedAt: h.now(), Events: []event.Event{ev}})
	if err != nil {
		span.SetStatus(codes.Error, "Slack event not stored")

		return fmt.Errorf("store the Slack event: %w", err)
	}

	h.metrics.Stored(intake, stored)

	s := stored[0]
	span.SetAttributes(attribute.StringSlice(attrEventKey, []string{s.Key}), attribute.Bool(attrDuplicate, s.Duplicate))

	log := h.log.With("intake", intake, "kind", string(ev.Kind), "duplicate", s.Duplicate)
	if !s.Duplicate {
		log = log.With("event_id", s.ID.String())
	}

	log.InfoContext(ctx, "Slack event stored")

	return nil
}

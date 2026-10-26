// Package webhook serves POST /hooks/{name}, where configured senders
// deliver alerts. For each delivery it reads the body up to MaxBodyBytes,
// authenticates it with the endpoint's bearer token or HMAC signature
// before anything parses it, lets the endpoint's adapter turn it into
// events, redacts them, and stores each one with a route_event job in one
// transaction.
//
// The route_event job's payload is {"event_id": "<the event's ID>"}.
package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/intake"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// Pattern is the route the handler serves, for http.ServeMux.
const Pattern = "POST /hooks/{name}"

// MaxBodyBytes is the largest body a delivery may have: 1 MiB.
const MaxBodyBytes = 1 << 20

// pathName is the wildcard in Pattern that holds the endpoint name.
const pathName = "name"

// The messages of error responses. They never name configured endpoints
// or say which check failed.
const (
	msgNotFound        = "not found"
	msgTooLarge        = "the body is larger than 1 MiB"
	msgUnreadable      = "the body could not be read"
	msgUnauthenticated = "unauthenticated"
	msgInvalidPayload  = "invalid payload: "
	msgInternal        = "internal error"
)

var (
	errNoAdapter = errors.New("no adapter was built for the endpoint")
	errNoAuth    = errors.New("the endpoint has neither bearer nor hmac set")
	errEmptyKey  = errors.New("key: empty")
	errBadKind   = errors.New("kind: must be alert, resolved, message or mention")
)

// knownKinds are the event kinds an adapter may return.
var knownKinds = []event.Kind{event.KindAlert, event.KindResolved, event.KindMessage, event.KindMention}

// Options set up a Handler. Every field is required.
type Options struct {
	// Webhooks are the configured endpoints, by name.
	Webhooks map[string]config.Webhook
	// Adapters are the adapter instances built for the endpoints, by
	// endpoint name.
	Adapters map[string]intake.Adapter
	// Secrets resolves the endpoints' bearer tokens and HMAC keys, on every
	// delivery, so a rotated secret is used without a restart.
	Secrets registry.Secrets
	// Redactor redacts events before they are stored.
	Redactor *redact.Redactor
	// Pool is the database the events and jobs are written to.
	Pool *pgxpool.Pool
	// Logger receives one line per delivery. Payloads are never logged.
	Logger *slog.Logger
}

// Handler serves webhook deliveries. Make it with New and register it at
// Pattern.
type Handler struct {
	endpoints map[string]endpoint
	secrets   registry.Secrets
	redactor  *redact.Redactor
	pool      *pgxpool.Pool
	log       *slog.Logger
}

// endpoint is one configured webhook endpoint with its adapter.
type endpoint struct {
	name    string
	cfg     config.Webhook
	adapter intake.Adapter
}

// New builds a Handler for the configured endpoints. Every endpoint must
// have an adapter.
func New(opts Options) (*Handler, error) {
	h := &Handler{
		endpoints: make(map[string]endpoint, len(opts.Webhooks)),
		secrets:   opts.Secrets,
		redactor:  opts.Redactor,
		pool:      opts.Pool,
		log:       opts.Logger,
	}

	for name, cfg := range opts.Webhooks {
		adapter, ok := opts.Adapters[name]
		if !ok {
			return nil, fmt.Errorf("webhook %q: %w", name, errNoAdapter)
		}

		if cfg.Auth.Bearer == nil && cfg.Auth.HMAC == nil {
			return nil, fmt.Errorf("webhook %q: %w", name, errNoAuth)
		}

		h.endpoints[name] = endpoint{name: name, cfg: cfg, adapter: adapter}
	}

	return h, nil
}

// ServeHTTP handles one delivery. It answers 202 with what was stored, 404
// for an endpoint that is not configured, 413 for a body over
// MaxBodyBytes, 401 when authentication fails, 400 when the adapter cannot
// parse the payload, and 500 when the delivery could not be stored.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	ep, ok := h.endpoints[r.PathValue(pathName)]
	if !ok {
		h.log.InfoContext(ctx, "webhook delivery for an unknown endpoint")
		h.writeError(ctx, w, http.StatusNotFound, msgNotFound)

		return
	}

	log := h.log.With("intake", ep.name)

	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		if _, tooLarge := errors.AsType[*http.MaxBytesError](err); tooLarge {
			log.InfoContext(ctx, "webhook delivery rejected: the body is too large")
			h.writeError(ctx, w, http.StatusRequestEntityTooLarge, msgTooLarge)

			return
		}

		log.InfoContext(ctx, "webhook delivery rejected: the body could not be read", "error", err.Error())
		h.writeError(ctx, w, http.StatusBadRequest, msgUnreadable)

		return
	}

	req := intake.Request{Endpoint: ep.name, Header: r.Header, Body: body, ReceivedAt: time.Now()}

	if err := h.authenticate(ctx, ep, req); err != nil {
		if !errors.Is(err, intake.ErrUnauthenticated) {
			log.ErrorContext(ctx, "webhook delivery not authenticated: the secret could not be read", "error", err.Error())
			h.writeError(ctx, w, http.StatusInternalServerError, msgInternal)

			return
		}

		log.InfoContext(ctx, "webhook delivery rejected: authentication failed", "error", err.Error())
		h.writeError(ctx, w, http.StatusUnauthorized, msgUnauthenticated)

		return
	}

	events, err := parse(ctx, ep, req)
	if err != nil {
		log.InfoContext(ctx, "webhook delivery rejected: the payload is not valid", "error", err.Error())
		h.writeError(ctx, w, http.StatusBadRequest, msgInvalidPayload+err.Error())

		return
	}

	stored, err := h.store(ctx, ep, req, events)
	if err != nil {
		log.ErrorContext(ctx, "webhook delivery not stored", "error", err.Error())
		h.writeError(ctx, w, http.StatusInternalServerError, msgInternal)

		return
	}

	resp := newResponse(stored)
	log.InfoContext(ctx, "webhook delivery accepted", "events", len(stored), "duplicate", resp.Duplicate)
	h.writeJSON(ctx, w, http.StatusAccepted, resp)
}

// authenticate resolves the endpoint's secret and checks the delivery with
// it: through the adapter's own scheme when it has one, otherwise with the
// configured bearer or HMAC check. An empty secret fails every delivery.
// Errors that do not wrap intake.ErrUnauthenticated mean the secret could
// not be read.
func (h *Handler) authenticate(ctx context.Context, ep endpoint, req intake.Request) error {
	auth := ep.cfg.Auth

	ref := auth.Bearer
	if ref == nil {
		ref = auth.HMAC
	}

	secret, err := secrets.ResolveText(ctx, h.secrets, *ref)
	if err != nil {
		return fmt.Errorf("resolve the endpoint's secret: %w", err)
	}

	if secret == "" {
		return fmt.Errorf("the endpoint's secret is empty: %w", intake.ErrUnauthenticated)
	}

	var authErr error

	switch verifier, ok := ep.adapter.(intake.SignatureVerifier); {
	case ok:
		if authErr = verifier.VerifySignature(req, []byte(secret)); authErr != nil && !errors.Is(authErr, intake.ErrUnauthenticated) {
			authErr = fmt.Errorf("%w: %w", intake.ErrUnauthenticated, authErr)
		}
	case auth.Bearer != nil:
		authErr = intake.Bearer{Token: []byte(secret)}.Authenticate(req)
	default:
		authErr = intake.HMAC{Secret: []byte(secret), Tolerance: auth.Tolerance}.Authenticate(req)
	}

	if authErr != nil {
		return fmt.Errorf("authenticate: %w", authErr)
	}

	return nil
}

// parse runs the endpoint's adapter and checks what it returns.
func parse(ctx context.Context, ep endpoint, req intake.Request) ([]event.Event, error) {
	events, err := ep.adapter.Parse(ctx, req)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}

	for i, ev := range events {
		switch {
		case ev.Key == "":
			return nil, fmt.Errorf("event %d: %w", i, errEmptyKey)
		case !slices.Contains(knownKinds, ev.Kind):
			return nil, fmt.Errorf("event %d: %w", i, errBadKind)
		}
	}

	return events, nil
}

// response is the body of a 202 answer.
type response struct {
	// Duplicate is true when the delivery held events and every one of
	// them was stored before.
	Duplicate bool             `json:"duplicate"`
	Events    []responseEntity `json:"events"`
}

// responseEntity says what happened to one event of the delivery.
type responseEntity struct {
	ID        string `json:"id,omitempty"`
	Status    string `json:"status,omitempty"`
	Duplicate bool   `json:"duplicate"`
}

func newResponse(stored []storedEvent) response {
	resp := response{Duplicate: len(stored) > 0, Events: make([]responseEntity, 0, len(stored))}

	for _, s := range stored {
		if s.duplicate {
			resp.Events = append(resp.Events, responseEntity{Duplicate: true})

			continue
		}

		resp.Duplicate = false
		resp.Events = append(resp.Events, responseEntity{ID: s.id.String(), Status: s.status})
	}

	return resp
}

// writeError answers with status and a JSON body that holds msg.
func (h *Handler) writeError(ctx context.Context, w http.ResponseWriter, status int, msg string) {
	h.writeJSON(ctx, w, status, map[string]string{"error": msg})
}

// writeJSON answers with status and v as JSON. v is always one of this
// package's own response types, which encode without error.
func (h *Handler) writeJSON(ctx context.Context, w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		h.log.ErrorContext(ctx, "encode the webhook response", "error", err.Error())
		http.Error(w, msgInternal, http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if _, err := w.Write(append(body, '\n')); err != nil {
		h.log.DebugContext(ctx, "write the webhook response", "error", err.Error())
	}
}

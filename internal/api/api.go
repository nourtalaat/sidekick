// Package api serves the JSON API under /api/v1, on the handlers that
// oapi-codegen generates from api/openapi.yaml into internal/api/apigen.
// Every request needs a session or an API token, each operation needs the
// role the spec names for it, and every error is problem details.
package api

import (
	"cmp"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	apispec "github.com/nourtalaat/sidekick/api"
	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/watch"
)

// BasePath is the path every API operation is under. Mount the handler New
// returns at BasePath + "/".
const BasePath = "/api/v1"

// maxBodyBytes is the largest request body the API reads.
const maxBodyBytes = 1 << 20

// errNoAuthenticator is returned by New without Options.Auth.
var errNoAuthenticator = errors.New("an authenticator is required")

// Options set up the API handler. Auth is required; each other field is
// required by the operations that use it, and sidekick serve sets them
// all.
type Options struct {
	// Auth finds the caller of each request.
	Auth Authenticator
	// Tokens creates, lists and revokes API tokens.
	Tokens TokenManager
	// Config is the loaded config file, for GET /config.
	Config *config.Config
	// Rules reads the runtime listeners and routes, for GET /config.
	Rules RuntimeRules
	// Redactor redacts the strings GET /config and GET /jobs show. Nil
	// shows them as they are.
	Redactor *redact.Redactor
	// Pool holds the jobs, for GET /jobs and the retry of a dead job.
	Pool *pgxpool.Pool
	// Audit records retried jobs.
	Audit *audit.Writer
	// Hub brings the database's notifications to the event streams. Run
	// it as a task. Without it, a stream answers 503.
	Hub *Hub
	// StreamHeartbeat is how often an event stream sends a comment and
	// reads the database for what a missed notification left out. Zero
	// means DefaultStreamHeartbeat.
	StreamHeartbeat time.Duration
	// StreamLifetime is how long an event stream stays open. Zero means
	// DefaultStreamLifetime.
	StreamLifetime time.Duration
	// Stopper stops watchers, for POST /watchers/{id}/stop. Without it,
	// that operation answers 503.
	Stopper *watch.Stopper
	// Watchers records the watchers that resolving an investigation's
	// group ends. Nil writes and shows the changes, but logs and counts
	// nothing.
	Watchers *watch.Recorder
	// Now returns the current time. Nil means time.Now.
	Now func() time.Time
	// Logger gets the errors that answer 500. Nil logs nothing.
	Logger *slog.Logger
}

// handler authenticates each request and passes it to the generated
// routes, answering paths and methods they do not serve with problem
// details.
type handler struct {
	auth   Authenticator
	routes *http.ServeMux
	log    *slog.Logger
}

// New returns the handler of every API operation. It reads the role of
// each operation from the spec, and fails if any operation has none.
func New(opts Options) (http.Handler, error) {
	if opts.Auth == nil {
		return nil, errNoAuthenticator
	}

	need, err := OperationRoles(apispec.Spec)
	if err != nil {
		return nil, fmt.Errorf("read the operation roles from the spec: %w", err)
	}

	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}

	now := opts.Now
	if now == nil {
		now = time.Now
	}

	h := &handler{auth: opts.Auth, routes: http.NewServeMux(), log: log}

	srv := &server{
		tokens: opts.Tokens, cfg: opts.Config, rules: opts.Rules, redactor: opts.Redactor, pool: opts.Pool, audit: opts.Audit,
		hub: opts.Hub, log: log, stopper: opts.Stopper, watchers: opts.Watchers, now: now,
		streamHeartbeat: cmp.Or(opts.StreamHeartbeat, DefaultStreamHeartbeat),
		streamLifetime:  cmp.Or(opts.StreamLifetime, DefaultStreamLifetime),
	}

	strict := apigen.NewStrictHandlerWithOptions(srv, []apigen.StrictMiddlewareFunc{requireRoles(need)},
		apigen.StrictHTTPServerOptions{
			RequestErrorHandlerFunc:  h.requestError,
			ResponseErrorHandlerFunc: h.responseError,
		})

	apigen.HandlerWithOptions(strict, apigen.StdHTTPServerOptions{
		BaseURL:          BasePath,
		BaseRouter:       h.routes,
		ErrorHandlerFunc: h.requestError,
	})

	return h, nil
}

// ServeHTTP authenticates r before anything else, so a caller without a
// session learns nothing about which paths exist.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)

	p, err := h.auth.Authenticate(r)
	if err != nil {
		h.authError(w, r, err)

		return
	}

	r = r.WithContext(auth.WithPrincipal(r.Context(), p))

	route, pattern := h.routes.Handler(r)
	if pattern == "" {
		h.unrouted(w, r, route)

		return
	}

	h.routes.ServeHTTP(w, r)
}

// authError answers a request that Authenticate refused.
func (h *handler) authError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, auth.ErrUnauthenticated):
		problem.Write(w, r, http.StatusUnauthorized, "Sign in, or send an API token.")
	case errors.Is(err, auth.ErrCSRF):
		problem.Write(w, r, http.StatusForbidden, "Send the session's CSRF token in the X-CSRF-Token header.")
	default:
		h.log.ErrorContext(r.Context(), "could not authenticate an API request", "path", r.URL.Path, "error", err)
		problem.Write(w, r, http.StatusInternalServerError, problem.InternalDetail)
	}
}

// unrouted answers a request no route serves. The mux's own handler
// decides between 404, 405 and a redirect to the clean path; a 404 or 405
// is answered as problem details instead of plain text.
func (h *handler) unrouted(w http.ResponseWriter, r *http.Request, route http.Handler) {
	probe := &statusProbe{header: http.Header{}}
	route.ServeHTTP(probe, r)

	switch probe.status {
	case http.StatusNotFound:
		problem.Write(w, r, http.StatusNotFound, "No API operation has this path.")
	case http.StatusMethodNotAllowed:
		w.Header().Set("Allow", probe.header.Get("Allow"))
		problem.Write(w, r, http.StatusMethodNotAllowed, "This path does not take this method.")
	default:
		route.ServeHTTP(w, r)
	}
}

// statusProbe is a ResponseWriter that keeps only the status and headers.
type statusProbe struct {
	header http.Header
	status int
}

func (p *statusProbe) Header() http.Header {
	return p.header
}

func (p *statusProbe) Write(b []byte) (int, error) {
	if p.status == 0 {
		p.status = http.StatusOK
	}

	return len(b), nil
}

func (p *statusProbe) WriteHeader(status int) {
	if p.status == 0 {
		p.status = status
	}
}

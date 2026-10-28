// Package api serves the JSON API under /api/v1, on the handlers that
// oapi-codegen generates from api/openapi.yaml into internal/api/apigen.
// Every request needs a session or an API token, each operation needs the
// role the spec names for it, and every error is problem details.
package api

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	apispec "github.com/nourtalaat/sidekick/api"
	"github.com/nourtalaat/sidekick/internal/api/apigen"
	"github.com/nourtalaat/sidekick/internal/auth"
)

// BasePath is the path every API operation is under. Mount the handler New
// returns at BasePath + "/".
const BasePath = "/api/v1"

// maxBodyBytes is the largest request body the API reads.
const maxBodyBytes = 1 << 20

// errNoAuthenticator is returned by New without Options.Auth.
var errNoAuthenticator = errors.New("an authenticator is required")

// Options set up the API handler. Auth is required.
type Options struct {
	// Auth finds the caller of each request.
	Auth Authenticator
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

	h := &handler{auth: opts.Auth, routes: http.NewServeMux(), log: log}

	strict := apigen.NewStrictHandlerWithOptions(&server{}, []apigen.StrictMiddlewareFunc{requireRoles(need)},
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
		writeProblem(w, r, http.StatusUnauthorized, "Sign in, or send an API token.")
	case errors.Is(err, auth.ErrCSRF):
		writeProblem(w, r, http.StatusForbidden, "Send the session's CSRF token in the X-CSRF-Token header.")
	default:
		h.log.ErrorContext(r.Context(), "could not authenticate an API request", "path", r.URL.Path, "error", err)
		writeProblem(w, r, http.StatusInternalServerError, internalDetail)
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
		writeProblem(w, r, http.StatusNotFound, "No API operation has this path.")
	case http.StatusMethodNotAllowed:
		w.Header().Set("Allow", probe.header.Get("Allow"))
		writeProblem(w, r, http.StatusMethodNotAllowed, "This path does not take this method.")
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

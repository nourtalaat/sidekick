package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/api/apigen"
)

// problemType is the problem type of every error: the status and the
// detail say what went wrong.
const problemType = "about:blank"

// internalDetail is the detail of a 500, which never shows the cause.
const internalDetail = "Something went wrong on the server. The logs have the details."

// Error is an error a handler returns to answer with problem details of
// its status. Any other error a handler returns answers 500.
type Error struct {
	Status int
	Detail string
}

func (e *Error) Error() string {
	return fmt.Sprintf("%d %s: %s", e.Status, http.StatusText(e.Status), e.Detail)
}

// problemf returns an Error with status and a detail made like fmt.Sprintf.
func problemf(status int, format string, args ...any) *Error {
	return &Error{Status: status, Detail: fmt.Sprintf(format, args...)}
}

// requestError answers a request whose parameters or body do not decode.
func (h *handler) requestError(w http.ResponseWriter, r *http.Request, err error) {
	if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeProblem(w, r, http.StatusRequestEntityTooLarge, fmt.Sprintf("The body is larger than %d bytes.", maxBodyBytes))

		return
	}

	writeProblem(w, r, http.StatusBadRequest, err.Error())
}

// responseError answers a request whose handler failed: with the status of
// an Error, or with 500.
func (h *handler) responseError(w http.ResponseWriter, r *http.Request, err error) {
	if e, ok := errors.AsType[*Error](err); ok {
		writeProblem(w, r, e.Status, e.Detail)

		return
	}

	h.log.ErrorContext(r.Context(), "an API request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	writeProblem(w, r, http.StatusInternalServerError, internalDetail)
}

// writeProblem answers r with problem details of status.
func writeProblem(w http.ResponseWriter, r *http.Request, status int, detail string) {
	instance := r.URL.Path

	body, err := json.Marshal(apigen.Problem{
		Type: problemType, Title: http.StatusText(status), Status: status, Detail: &detail, Instance: &instance,
	})
	if err != nil {
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

		return
	}

	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

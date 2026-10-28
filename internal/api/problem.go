package api

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/nourtalaat/sidekick/internal/problem"
)

// Error is an error a handler returns to answer with problem details of
// its status. Any other error a handler returns answers 500.
type Error struct {
	Status int
	Detail string
	// Code names the kind of problem for a program; empty for none.
	Code string
	// Fields lists the fields of the request that were refused.
	Fields []problem.FieldError
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
		problem.Write(w, r, http.StatusRequestEntityTooLarge, fmt.Sprintf("The body is larger than %d bytes.", maxBodyBytes))

		return
	}

	problem.Write(w, r, http.StatusBadRequest, err.Error())
}

// responseError answers a request whose handler failed: with the status of
// an Error, or with 500.
func (h *handler) responseError(w http.ResponseWriter, r *http.Request, err error) {
	if e, ok := errors.AsType[*Error](err); ok {
		problem.WriteCoded(w, r, e.Status, e.Detail, e.Code, e.Fields)

		return
	}

	h.log.ErrorContext(r.Context(), "an API request failed", "method", r.Method, "path", r.URL.Path, "error", err)
	problem.Write(w, r, http.StatusInternalServerError, problem.InternalDetail)
}

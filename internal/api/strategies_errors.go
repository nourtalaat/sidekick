package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/problem"
	"github.com/nourtalaat/sidekick/internal/strategystore"
)

// codeUnchanged is the code of the problem of a save whose body is the same
// as the current version.
const codeUnchanged = "unchanged"

// The fields of a strategy save that problems point at.
const (
	fieldBody   = "body"
	fieldReason = "reason"
)

// errStrategiesUnavailable answers a request when the server was started
// without a strategy store.
var errStrategiesUnavailable = problemf(http.StatusServiceUnavailable, "Runtime strategies are not available.")

// strategyNotFound is the 404 answer for a name that no strategy has.
func strategyNotFound(name string) *Error {
	return &Error{Status: http.StatusNotFound, Code: codeNotFound, Detail: fmt.Sprintf("No strategy is named %s.", name)}
}

// strategyError is the answer to a failed save of the strategy name. An
// error it does not know answers 500.
func strategyError(err error, name string) error {
	var invalid *strategystore.InvalidError

	switch {
	case errors.As(err, &invalid):
		return invalidStrategyError(invalid)
	case errors.Is(err, strategystore.ErrUnchanged):
		return &Error{
			Status: http.StatusUnprocessableEntity, Code: codeUnchanged,
			Detail: "The strategy is the same as its current version, so there is nothing to save.",
			Fields: []problem.FieldError{{Field: fieldBody, Message: "The strategy is the same as its current version."}},
		}
	case errors.Is(err, strategystore.ErrStale):
		return &Error{
			Status: http.StatusConflict, Code: codeStale,
			Detail: fmt.Sprintf("The strategy %s was saved again since you read it. Reload it to see the new version.", name),
		}
	case errors.Is(err, strategystore.ErrConflict):
		return &Error{
			Status: http.StatusConflict, Code: codeConflict,
			Detail: fmt.Sprintf("The strategy %s was created at the same moment by someone else. Try again.", name),
		}
	default:
		return fmt.Errorf("save strategy %s: %w", name, err)
	}
}

// invalidStrategyError is the 422 answer to a save that fails the checks,
// with a field for each problem: reason, body for the whole YAML, or body.
// and the path in the YAML.
func invalidStrategyError(invalid *strategystore.InvalidError) *Error {
	var fields []problem.FieldError

	if invalid.Reason != "" {
		fields = append(fields, problem.FieldError{Field: fieldReason, Message: invalid.Reason})
	}

	for _, p := range invalid.Body {
		field := fieldBody
		if p.Path != "" {
			field += "." + p.Path
		}

		message := p.Message
		if p.Line > 0 {
			message += " (line " + strconv.Itoa(p.Line) + ")"
		}

		fields = append(fields, problem.FieldError{Field: field, Message: message})
	}

	lines := make([]string, len(fields))
	for i, f := range fields {
		lines[i] = f.Field + ": " + f.Message
	}

	return &Error{
		Status: http.StatusUnprocessableEntity, Code: codeValidation, Fields: fields,
		Detail: "The strategy does not pass the checks:\n" + strings.Join(lines, "\n"),
	}
}

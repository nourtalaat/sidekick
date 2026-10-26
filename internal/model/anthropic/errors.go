package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	anthropic "github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/shared"

	"github.com/nourtalaat/sidekick/internal/model"
)

// statusOverloaded is the status Anthropic returns when the API is
// overloaded.
const statusOverloaded = 529

// contextLengthPhrases are in the message of an invalid_request_error that
// says the prompt does not fit the context window.
var contextLengthPhrases = []string{"prompt is too long", "context window", "context length"}

// kindsByType classifies API errors by their error type.
var kindsByType = map[shared.ErrorType]model.ErrorKind{
	shared.ErrorTypeInvalidRequestError: model.ErrInvalid,
	shared.ErrorTypeNotFoundError:       model.ErrInvalid,
	shared.ErrorTypeAuthenticationError: model.ErrAuth,
	shared.ErrorTypePermissionError:     model.ErrAuth,
	// A billing problem is an account problem the operator must fix, like
	// a bad key; retrying does not help.
	shared.ErrorTypeBillingError:    model.ErrAuth,
	shared.ErrorTypeRateLimitError:  model.ErrRateLimited,
	shared.ErrorTypeTimeoutError:    model.ErrTimeout,
	shared.ErrorTypeOverloadedError: model.ErrOverloaded,
	// An internal server error is a passing failure of the service, so it
	// is retried like an overload.
	shared.ErrorTypeAPIError: model.ErrOverloaded,
}

// kindsByStatus classifies API errors whose body names no known type.
var kindsByStatus = map[int]model.ErrorKind{
	http.StatusBadRequest:            model.ErrInvalid,
	http.StatusNotFound:              model.ErrInvalid,
	http.StatusRequestEntityTooLarge: model.ErrInvalid,
	http.StatusUnprocessableEntity:   model.ErrInvalid,
	http.StatusUnauthorized:          model.ErrAuth,
	http.StatusPaymentRequired:       model.ErrAuth,
	http.StatusForbidden:             model.ErrAuth,
	http.StatusRequestTimeout:        model.ErrTimeout,
	http.StatusGatewayTimeout:        model.ErrTimeout,
	http.StatusTooManyRequests:       model.ErrRateLimited,
	http.StatusInternalServerError:   model.ErrOverloaded,
	http.StatusBadGateway:            model.ErrOverloaded,
	http.StatusServiceUnavailable:    model.ErrOverloaded,
	statusOverloaded:                 model.ErrOverloaded,
}

// classify turns a failed call into a *model.Error, with the wait the API
// asked for when it sent one.
func classify(err error) *model.Error {
	var apiErr *anthropic.Error
	if errors.As(err, &apiErr) {
		return &model.Error{Kind: apiKind(apiErr), RetryAfter: model.RetryAfter(apiErr.Response), Err: err}
	}

	var netErr net.Error

	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return &model.Error{Kind: model.ErrTimeout, Err: err}
	default:
		return &model.Error{Kind: model.ErrUnknown, Err: err}
	}
}

// apiKind classifies an API error by its type, then by its status.
func apiKind(e *anthropic.Error) model.ErrorKind {
	kind, ok := kindsByType[e.Type()]
	if !ok {
		kind, ok = kindsByStatus[e.StatusCode]
	}

	switch {
	case !ok:
		return model.ErrUnknown
	case kind == model.ErrInvalid && isContextLength(e.RawJSON()):
		return model.ErrContextLength
	default:
		return kind
	}
}

// isContextLength reports whether an error body says the prompt is too
// long for the model.
func isContextLength(body string) bool {
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}

	if json.Unmarshal([]byte(body), &envelope) != nil {
		return false
	}

	msg := strings.ToLower(envelope.Error.Message)
	for _, phrase := range contextLengthPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}

	return false
}

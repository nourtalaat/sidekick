package google

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Detail types and fields of a Google API error that the connector reads.
const (
	detailType      = "@type"
	retryInfoType   = "type.googleapis.com/google.rpc.RetryInfo"
	retryDelayField = "retryDelay"
	errorInfoType   = "type.googleapis.com/google.rpc.ErrorInfo"
	reasonField     = "reason"
)

// contextLengthPhrases are in the message of an INVALID_ARGUMENT error that
// says the prompt does not fit the context window, for example "The input
// token count (1200000) exceeds the maximum number of tokens allowed
// (1048576)".
var contextLengthPhrases = []string{"exceeds the maximum number of tokens", "input token count", "context window", "prompt is too long"}

// kindsByReason classifies errors by the reason in their ErrorInfo detail.
// The Gemini API answers a bad or blocked API key with INVALID_ARGUMENT,
// which would otherwise read as a bad request.
var kindsByReason = map[string]model.ErrorKind{
	"API_KEY_INVALID":         model.ErrAuth,
	"API_KEY_EXPIRED":         model.ErrAuth,
	"API_KEY_SERVICE_BLOCKED": model.ErrAuth,
	"SERVICE_DISABLED":        model.ErrAuth,
}

// kindsByStatus classifies errors by their google.rpc status, from the
// Gemini API troubleshooting guide and the Vertex AI error codes.
var kindsByStatus = map[string]model.ErrorKind{
	"INVALID_ARGUMENT": model.ErrInvalid,
	"NOT_FOUND":        model.ErrInvalid,
	"OUT_OF_RANGE":     model.ErrInvalid,
	// The free tier is not offered in the caller's country, or billing is
	// not set up: the operator must fix the account, as with a bad key.
	"FAILED_PRECONDITION": model.ErrAuth,
	"UNAUTHENTICATED":     model.ErrAuth,
	"PERMISSION_DENIED":   model.ErrAuth,
	"RESOURCE_EXHAUSTED":  model.ErrRateLimited,
	"UNAVAILABLE":         model.ErrOverloaded,
	"INTERNAL":            model.ErrOverloaded,
	"ABORTED":             model.ErrOverloaded,
	"DEADLINE_EXCEEDED":   model.ErrTimeout,
}

// kindsByCode classifies errors whose status is not known, by HTTP status.
var kindsByCode = map[int]model.ErrorKind{
	http.StatusBadRequest:            model.ErrInvalid,
	http.StatusNotFound:              model.ErrInvalid,
	http.StatusRequestEntityTooLarge: model.ErrInvalid,
	http.StatusUnauthorized:          model.ErrAuth,
	http.StatusForbidden:             model.ErrAuth,
	http.StatusRequestTimeout:        model.ErrTimeout,
	http.StatusGatewayTimeout:        model.ErrTimeout,
	http.StatusTooManyRequests:       model.ErrRateLimited,
	http.StatusInternalServerError:   model.ErrOverloaded,
	http.StatusBadGateway:            model.ErrOverloaded,
	http.StatusServiceUnavailable:    model.ErrOverloaded,
}

// classify turns a failed call into a *model.Error, with the wait the API
// asked for when it sent one.
func classify(err error) *model.Error {
	var (
		apiErr  genai.APIError
		credErr *credentialError
		netErr  net.Error
	)

	switch {
	case errors.As(err, &apiErr):
		return &model.Error{Kind: apiKind(apiErr), RetryAfter: retryAfter(apiErr), Err: err}
	case errors.As(err, &credErr):
		return &model.Error{Kind: model.ErrAuth, Err: err}
	case errors.Is(err, errIncomplete), errors.Is(err, errContinuation):
		// The server gave up part way; a retry can fix it.
		return &model.Error{Kind: model.ErrOverloaded, Err: err}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return &model.Error{Kind: model.ErrTimeout, Err: err}
	default:
		return &model.Error{Kind: model.ErrUnknown, Err: err}
	}
}

// apiKind classifies an API error by its reason, then its status, then
// its HTTP status. An invalid request whose message says the prompt is too
// long is a context length error.
func apiKind(e genai.APIError) model.ErrorKind {
	if kind, ok := kindsByReason[detailField(e, errorInfoType, reasonField)]; ok {
		return kind
	}

	kind, ok := kindsByStatus[e.Status]
	if !ok {
		kind, ok = kindsByCode[e.Code]
	}

	switch {
	case !ok:
		return model.ErrUnknown
	case kind == model.ErrInvalid && isContextLength(e.Message):
		return model.ErrContextLength
	default:
		return kind
	}
}

// isContextLength reports whether an error message says the prompt does
// not fit the context window.
func isContextLength(message string) bool {
	msg := strings.ToLower(message)
	for _, phrase := range contextLengthPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}

	return false
}

// retryAfter reads how long the API asked to wait from the google.rpc
// RetryInfo detail of an error, which Google sends in the body instead of a
// retry-after header. It returns zero when there is none. The result goes
// in model.Error.RetryAfter, where model.Generate treats it as it treats
// the wait other connectors read with model.RetryAfter.
func retryAfter(e genai.APIError) time.Duration {
	wait, err := time.ParseDuration(detailField(e, retryInfoType, retryDelayField))
	if err != nil || wait <= 0 {
		return 0
	}

	return wait
}

// detailField returns a string field of the first error detail of a type,
// or "" when there is none.
func detailField(e genai.APIError, typ, field string) string {
	for _, d := range e.Details {
		if d[detailType] != typ {
			continue
		}

		if v, ok := d[field].(string); ok {
			return v
		}
	}

	return ""
}

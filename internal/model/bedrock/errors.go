package bedrock

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"

	"github.com/nourtalaat/sidekick/internal/model"
)

// retryAfterHeader is the response header that says how long to wait
// before retrying, in seconds or as an HTTP date.
const retryAfterHeader = "Retry-After"

// codeValidation is the error code of a request Bedrock cannot take.
const codeValidation = "ValidationException"

// contextLengthPhrases are in the message of a ValidationException that
// says the prompt does not fit the context window.
var contextLengthPhrases = []string{"prompt is too long", "input is too long", "too many input tokens", "context window", "context length"}

// kindsByCode classifies Bedrock errors by their error code, from the
// Converse and ConverseStream API references and the AWS common errors.
var kindsByCode = map[string]model.ErrorKind{
	codeValidation:                  model.ErrInvalid,
	"ResourceNotFoundException":     model.ErrInvalid,
	"AccessDeniedException":         model.ErrAuth,
	"UnrecognizedClientException":   model.ErrAuth,
	"InvalidSignatureException":     model.ErrAuth,
	"ExpiredTokenException":         model.ErrAuth,
	"IncompleteSignature":           model.ErrAuth,
	"MissingAuthenticationToken":    model.ErrAuth,
	"ThrottlingException":           model.ErrRateLimited,
	"ServiceQuotaExceededException": model.ErrRateLimited,
	// The model is still being loaded; Bedrock says to retry.
	"ModelNotReadyException":      model.ErrOverloaded,
	"ServiceUnavailableException": model.ErrOverloaded,
	"InternalServerException":     model.ErrOverloaded,
	// The model failed while answering; a retry can succeed.
	"ModelErrorException":       model.ErrOverloaded,
	"ModelStreamErrorException": model.ErrOverloaded,
	"ModelTimeoutException":     model.ErrTimeout,
}

// kindsByStatus classifies API errors whose code is not known.
var kindsByStatus = map[int]model.ErrorKind{
	http.StatusBadRequest:          model.ErrInvalid,
	http.StatusNotFound:            model.ErrInvalid,
	http.StatusUnauthorized:        model.ErrAuth,
	http.StatusForbidden:           model.ErrAuth,
	http.StatusRequestTimeout:      model.ErrTimeout,
	http.StatusGatewayTimeout:      model.ErrTimeout,
	http.StatusTooManyRequests:     model.ErrRateLimited,
	http.StatusInternalServerError: model.ErrOverloaded,
	http.StatusBadGateway:          model.ErrOverloaded,
	http.StatusServiceUnavailable:  model.ErrOverloaded,
}

// classify turns a failed call into a *model.Error, with the wait the API
// asked for when it sent one.
func classify(err error) *model.Error {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		return &model.Error{Kind: apiKind(apiErr, statusOf(err)), RetryAfter: retryAfter(err), Err: err}
	}

	if status := statusOf(err); status != 0 {
		if kind, ok := kindsByStatus[status]; ok {
			return &model.Error{Kind: kind, RetryAfter: retryAfter(err), Err: err}
		}
	}

	var netErr net.Error

	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return &model.Error{Kind: model.ErrTimeout, Err: err}
	default:
		return &model.Error{Kind: model.ErrUnknown, Err: err}
	}
}

// apiKind classifies an API error by its code, then by its HTTP status.
func apiKind(e smithy.APIError, status int) model.ErrorKind {
	kind, ok := kindsByCode[e.ErrorCode()]
	if !ok {
		kind, ok = kindsByStatus[status]
	}

	switch {
	case !ok:
		return model.ErrUnknown
	case kind == model.ErrInvalid && isContextLength(e.ErrorMessage()):
		return model.ErrContextLength
	default:
		return kind
	}
}

// isContextLength reports whether an error message says the prompt is too
// long for the model.
func isContextLength(msg string) bool {
	msg = strings.ToLower(msg)
	for _, phrase := range contextLengthPhrases {
		if strings.Contains(msg, phrase) {
			return true
		}
	}

	return false
}

// statusOf returns the HTTP status of a failed call, or zero when there was
// no response.
func statusOf(err error) int {
	var respErr *awshttp.ResponseError
	if errors.As(err, &respErr) {
		return respErr.HTTPStatusCode()
	}

	return 0
}

// retryAfter reads how long the API asked to wait, in seconds or as an HTTP
// date. It returns zero when it asked for nothing.
func retryAfter(err error) time.Duration {
	var respErr *awshttp.ResponseError
	if !errors.As(err, &respErr) || respErr.Response == nil {
		return 0
	}

	v := respErr.Response.Header.Get(retryAfterHeader)
	if v == "" {
		return 0
	}

	if secs, perr := strconv.ParseFloat(v, 64); perr == nil {
		if secs <= 0 {
			return 0
		}

		return time.Duration(secs * float64(time.Second))
	}

	if at, perr := http.ParseTime(v); perr == nil {
		if d := time.Until(at); d > 0 {
			return d
		}
	}

	return 0
}

package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/packages/ssestream"

	"github.com/nourtalaat/sidekick/internal/model"
)

// maxErrorBody is the most of an error response body the connector reads.
const maxErrorBody = 64 << 10

// contextLengthPhrases are in the error messages of OpenAI, vLLM and the
// llama.cpp server when the prompt does not fit the context window.
var contextLengthPhrases = []string{"context length", "context_length", "context window", "context size", "maximum context"}

// kindsByCode classifies errors by their error code. OpenAI sends these
// codes in HTTP errors, in streamed error events and on failed responses.
var kindsByCode = map[string]model.ErrorKind{
	"context_length_exceeded": model.ErrContextLength,
	"rate_limit_exceeded":     model.ErrRateLimited,
	// Running out of quota or credit is an account problem the operator
	// must fix, like a bad key; retrying does not help.
	"insufficient_quota": model.ErrAuth,
	"invalid_api_key":    model.ErrAuth,
	"server_error":       model.ErrOverloaded,
	"invalid_prompt":     model.ErrInvalid,
}

// kindsByStatus classifies API errors whose code is not in kindsByCode.
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
}

// failedError is a failure reported inside a stream: an error event or a
// failed response.
type failedError struct {
	code    string
	message string
}

// Error implements error.
func (e *failedError) Error() string {
	return fmt.Sprintf("response failed: %s: %s", e.code, e.message)
}

// buildError is a request the connector could not build from the shared
// request, such as a tool with a schema that is not JSON.
type buildError struct {
	err error
}

// Error implements error.
func (e *buildError) Error() string {
	return "build request: " + e.err.Error()
}

// Unwrap returns the cause.
func (e *buildError) Unwrap() error {
	return e.err
}

// classify turns a failed call into a *model.Error, with the wait the API
// asked for when it sent one.
func classify(err error) *model.Error {
	var (
		built     *buildError
		apiErr    *openai.Error
		failed    *failedError
		streamErr *ssestream.StreamError
		netErr    net.Error
	)

	switch {
	case errors.As(err, &built):
		return &model.Error{Kind: model.ErrInvalid, Err: err}
	case errors.As(err, &apiErr):
		return &model.Error{Kind: apiKind(apiErr), RetryAfter: model.RetryAfter(apiErr.Response), Err: err}
	case errors.As(err, &failed):
		return &model.Error{Kind: kindOf(failed.code, 0, failed.message), Err: err}
	case errors.As(err, &streamErr):
		code, message := errorFields(streamErr.Event.Data)

		return &model.Error{Kind: kindOf(code, 0, message), Err: err}
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &netErr) && netErr.Timeout():
		return &model.Error{Kind: model.ErrTimeout, Err: err}
	default:
		return &model.Error{Kind: model.ErrUnknown, Err: err}
	}
}

// apiKind classifies an HTTP error from the API. When the SDK found no
// error object, the fields are read from the body itself.
func apiKind(e *openai.Error) model.ErrorKind {
	code, message := e.Code, e.Message
	if code == "" && message == "" {
		code, message = errorFields(responseBody(e.Response))
	}

	return kindOf(code, e.StatusCode, message)
}

// kindOf classifies an error by its code, then its HTTP status when there
// is one, then its message. An invalid request whose message says the
// prompt is too long is a context length error.
func kindOf(code string, status int, message string) model.ErrorKind {
	kind, ok := kindsByCode[code]
	if !ok {
		kind, ok = kindsByStatus[status]
	}

	switch {
	case (!ok || kind == model.ErrInvalid) && isContextLength(message):
		return model.ErrContextLength
	case !ok && status == 0:
		// A failure inside a stream with no known code is most often the
		// server giving up part way, which a retry can fix.
		return model.ErrOverloaded
	case !ok:
		return model.ErrUnknown
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

// errorFields reads the code and message of an error body. OpenAI and most
// compatible servers wrap the error in an "error" object; some vLLM
// versions send its fields at the top level. A code that is not a string,
// such as llama.cpp's HTTP status number, is replaced by the error type.
func errorFields(data []byte) (code, message string) {
	type fields struct {
		Code    json.RawMessage `json:"code"`
		Type    string          `json:"type"`
		Message string          `json:"message"`
	}

	var body struct {
		fields

		Error *fields `json:"error"`
	}

	if json.Unmarshal(data, &body) != nil {
		return "", ""
	}

	f := body.fields
	if body.Error != nil {
		f = *body.Error
	}

	if err := json.Unmarshal(f.Code, &code); err != nil || code == "" {
		code = f.Type
	}

	return code, f.Message
}

// responseBody reads up to maxErrorBody bytes of an error response, which
// the SDK keeps readable after it parsed the body.
func responseBody(resp *http.Response) []byte {
	if resp == nil || resp.Body == nil {
		return nil
	}

	data, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBody))
	if err != nil {
		return nil
	}

	return data
}

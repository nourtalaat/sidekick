package openai

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/openai/openai-go/v3/packages/ssestream"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// timeoutError is a network error that timed out.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want model.ErrorKind
	}{
		{name: "deadline exceeded", err: fmt.Errorf("stream: %w", context.DeadlineExceeded), want: model.ErrTimeout},
		{name: "network timeout", err: fmt.Errorf("dial: %w", timeoutError{}), want: model.ErrTimeout},
		{name: "request that cannot be built", err: &buildError{err: errors.New("bad schema")}, want: model.ErrInvalid},
		{name: "failed response with a known code", err: &failedError{code: "rate_limit_exceeded"}, want: model.ErrRateLimited},
		{name: "failed response with an unknown code", err: &failedError{code: "vector_store_timeout"}, want: model.ErrOverloaded},
		{name: "failed response that is too long", err: &failedError{code: "bad_request", message: "Input exceeds the context window."}, want: model.ErrContextLength},
		{
			name: "stream error event with a numeric code",
			err:  &ssestream.StreamError{Event: ssestream.Event{Data: []byte(`{"error":{"message":"boom","type":"InternalServerError","code":500}}`)}},
			want: model.ErrOverloaded,
		},
		{name: "canceled", err: context.Canceled, want: model.ErrUnknown},
		{name: "anything else", err: errors.New("boom"), want: model.ErrUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classify(tt.err)
			require.Equal(t, tt.want, got.Kind)
			require.ErrorIs(t, got, tt.err)
		})
	}
}

func TestErrorFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		body        string
		wantCode    string
		wantMessage string
	}{
		{name: "OpenAI envelope", body: `{"error":{"message":"m","type":"invalid_request_error","code":"context_length_exceeded"}}`, wantCode: "context_length_exceeded", wantMessage: "m"},
		{name: "null code falls back to the type", body: `{"error":{"message":"m","type":"server_error","code":null}}`, wantCode: "server_error", wantMessage: "m"},
		{name: "numeric code falls back to the type", body: `{"error":{"message":"m","type":"exceed_context_size_error","code":400}}`, wantCode: "exceed_context_size_error", wantMessage: "m"},
		{name: "fields at the top level", body: `{"object":"error","message":"m","type":"BadRequestError","code":400}`, wantCode: "BadRequestError", wantMessage: "m"},
		{name: "not JSON", body: `<html>`},
		{name: "empty"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			code, message := errorFields([]byte(tt.body))
			require.Equal(t, tt.wantCode, code)
			require.Equal(t, tt.wantMessage, message)
		})
	}
}

func TestToolInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		arguments string
		want      string
	}{
		{name: "JSON object", arguments: `{"a": 1}`, want: `{"a": 1}`},
		{name: "empty", arguments: "", want: `{}`},
		{name: "blank", arguments: "  ", want: `{}`},
		{name: "not JSON is kept as a string", arguments: `{"a": 1`, want: `"{\"a\": 1"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.JSONEq(t, tt.want, string(toolInput(tt.arguments)))
		})
	}
}

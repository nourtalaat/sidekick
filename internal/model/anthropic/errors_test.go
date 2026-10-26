package anthropic

import (
	"context"
	"errors"
	"fmt"
	"testing"

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

func TestIsContextLength(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
		want bool
	}{
		{name: "prompt too long", body: `{"error":{"type":"invalid_request_error","message":"prompt is too long: 210000 tokens > 200000 maximum"}}`, want: true},
		{name: "another invalid request", body: `{"error":{"type":"invalid_request_error","message":"max_tokens: must be positive"}}`},
		{name: "not JSON", body: `<html>`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, isContextLength(tt.body))
		})
	}
}

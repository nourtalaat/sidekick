package anthropic

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

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

func TestRetryAfter(t *testing.T) {
	t.Parallel()

	future := time.Now().Add(time.Hour).UTC().Format(http.TimeFormat)
	past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)

	tests := []struct {
		name    string
		headers map[string]string
		wantMin time.Duration
		wantMax time.Duration
	}{
		{name: "no headers"},
		{name: "seconds", headers: map[string]string{retryAfterHeader: "12"}, wantMin: 12 * time.Second, wantMax: 12 * time.Second},
		{name: "fractional seconds", headers: map[string]string{retryAfterHeader: "1.5"}, wantMin: 1500 * time.Millisecond, wantMax: 1500 * time.Millisecond},
		{
			name:    "milliseconds win over seconds",
			headers: map[string]string{retryAfterMSHeader: "250", retryAfterHeader: "12"},
			wantMin: 250 * time.Millisecond,
			wantMax: 250 * time.Millisecond,
		},
		{name: "an HTTP date", headers: map[string]string{retryAfterHeader: future}, wantMin: 59 * time.Minute, wantMax: time.Hour},
		{name: "a date in the past", headers: map[string]string{retryAfterHeader: past}},
		{name: "zero", headers: map[string]string{retryAfterHeader: "0"}},
		{name: "garbage", headers: map[string]string{retryAfterHeader: "soon"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			resp := &http.Response{Header: http.Header{}}
			for k, v := range tt.headers {
				resp.Header.Set(k, v)
			}

			got := retryAfter(resp)
			require.GreaterOrEqual(t, got, tt.wantMin)
			require.LessOrEqual(t, got, tt.wantMax)
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

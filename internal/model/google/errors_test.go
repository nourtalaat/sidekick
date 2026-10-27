package google

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/genai"

	"github.com/nourtalaat/sidekick/internal/model"
)

func TestClassify(t *testing.T) {
	t.Parallel()

	retryIn := func(delay string) []map[string]any {
		return []map[string]any{{detailType: retryInfoType, retryDelayField: delay}}
	}

	tests := []struct {
		name           string
		err            error
		wantKind       model.ErrorKind
		wantRetryAfter time.Duration
	}{
		{name: "rate limited with a retry delay", err: genai.APIError{Code: http.StatusTooManyRequests, Status: "RESOURCE_EXHAUSTED", Details: retryIn("1.5s")}, wantKind: model.ErrRateLimited, wantRetryAfter: 1500 * time.Millisecond},
		{name: "retry delay that is not a duration", err: genai.APIError{Code: http.StatusTooManyRequests, Status: "RESOURCE_EXHAUSTED", Details: retryIn("soon")}, wantKind: model.ErrRateLimited},
		{name: "bad key", err: genai.APIError{Code: http.StatusBadRequest, Status: "INVALID_ARGUMENT", Details: []map[string]any{{detailType: errorInfoType, reasonField: "API_KEY_INVALID"}}}, wantKind: model.ErrAuth},
		{name: "unauthenticated", err: genai.APIError{Code: http.StatusUnauthorized, Status: "UNAUTHENTICATED"}, wantKind: model.ErrAuth},
		{name: "internal", err: genai.APIError{Code: http.StatusInternalServerError, Status: "INTERNAL"}, wantKind: model.ErrOverloaded},
		{name: "deadline", err: genai.APIError{Code: http.StatusGatewayTimeout, Status: "DEADLINE_EXCEEDED"}, wantKind: model.ErrTimeout},
		{name: "not found", err: genai.APIError{Code: http.StatusNotFound, Status: "NOT_FOUND"}, wantKind: model.ErrInvalid},
		// A body that is not JSON leaves the status line as the status.
		{name: "status line only", err: genai.APIError{Code: http.StatusBadGateway, Status: "502 Bad Gateway"}, wantKind: model.ErrOverloaded},
		{name: "unknown status", err: genai.APIError{Code: 499, Status: "CANCELLED"}, wantKind: model.ErrUnknown},
		{name: "credentials", err: &credentialError{err: errors.New("no token")}, wantKind: model.ErrAuth},
		{name: "stream cut short", err: errIncomplete, wantKind: model.ErrOverloaded},
		{name: "continuation asked for", err: errContinuation, wantKind: model.ErrOverloaded},
		{name: "deadline of the call", err: context.DeadlineExceeded, wantKind: model.ErrTimeout},
		{name: "anything else", err: errors.New("boom"), wantKind: model.ErrUnknown},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classify(fmt.Errorf("connector gemini: %w", tt.err))
			require.Equal(t, tt.wantKind, got.Kind)
			require.Equal(t, tt.wantRetryAfter, got.RetryAfter)
			// The cause is kept. errors.Is cannot compare an APIError,
			// whose details make it not comparable.
			require.Equal(t, tt.err, errors.Unwrap(got.Err))
		})
	}
}

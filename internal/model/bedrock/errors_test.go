package bedrock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
)

// retryAfterSlack is how far a wait read from an HTTP date may be from
// the expected one: the date has whole seconds, and a slow machine takes
// time between writing the date and reading it back.
const retryAfterSlack = 5 * time.Second

// timeoutError is a net.Error that timed out.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

func TestClassify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		err            error
		wantKind       model.ErrorKind
		wantRetryAfter time.Duration
	}{
		{
			name:     "a throttling exception from the stream",
			err:      &types.ThrottlingException{Message: aws.String("slow down")},
			wantKind: model.ErrRateLimited,
		},
		{
			name:     "a service quota exception",
			err:      &types.ServiceQuotaExceededException{Message: aws.String("quota")},
			wantKind: model.ErrRateLimited,
		},
		{
			name:     "a model that is not ready yet",
			err:      &types.ModelNotReadyException{Message: aws.String("loading")},
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "a validation exception about the context window",
			err:      &types.ValidationException{Message: aws.String("prompt is too long: 1200000 tokens > 1000000 maximum")},
			wantKind: model.ErrContextLength,
		},
		{
			name:     "a resource that does not exist",
			err:      &types.ResourceNotFoundException{Message: aws.String("model not found")},
			wantKind: model.ErrInvalid,
		},
		{
			name:           "a retry-after as an HTTP date",
			err:            responseError(http.StatusTooManyRequests, time.Now().Add(time.Hour).UTC().Format(http.TimeFormat), &types.ThrottlingException{}),
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: time.Hour,
		},
		{
			name:     "a retry-after that is not a number or a date",
			err:      responseError(http.StatusTooManyRequests, "soon", &types.ThrottlingException{}),
			wantKind: model.ErrRateLimited,
		},
		{
			name:     "an unknown code with an unknown status",
			err:      responseError(http.StatusTeapot, "", &smithy.GenericAPIError{Code: "Teapot"}),
			wantKind: model.ErrUnknown,
		},
		{
			name:     "a status without an API error",
			err:      responseError(http.StatusBadGateway, "", errors.New("bad gateway")),
			wantKind: model.ErrOverloaded,
		},
		{
			name:     "the call ran out of time",
			err:      fmt.Errorf("converse: %w", context.DeadlineExceeded),
			wantKind: model.ErrTimeout,
		},
		{
			name:     "a network timeout",
			err:      fmt.Errorf("converse: %w", timeoutError{}),
			wantKind: model.ErrTimeout,
		},
		{
			name:     "anything else",
			err:      errors.New("connection reset by peer"),
			wantKind: model.ErrUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := classify(tt.err)
			require.Equal(t, tt.wantKind, got.Kind)
			require.ErrorIs(t, got, tt.err)
			require.InDelta(t, tt.wantRetryAfter, got.RetryAfter, float64(retryAfterSlack))
		})
	}
}

// responseError wraps err in the SDK's error for an HTTP response with
// status and, when set, a Retry-After header.
func responseError(status int, retryAfterValue string, err error) error {
	resp := &http.Response{StatusCode: status, Header: http.Header{}}
	if retryAfterValue != "" {
		resp.Header.Set(retryAfterHeader, retryAfterValue)
	}

	return &awshttp.ResponseError{ResponseError: &smithyhttp.ResponseError{Response: &smithyhttp.Response{Response: resp}, Err: err}}
}

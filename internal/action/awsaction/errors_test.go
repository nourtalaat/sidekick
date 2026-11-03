package awsaction

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"
	smithyhttp "github.com/aws/smithy-go/transport/http"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
)

// apiError is an answer of AWS with a code and a fault.
func apiError(code string, fault smithy.ErrorFault, status int) error {
	return &awshttp.ResponseError{
		ResponseError: &smithyhttp.ResponseError{
			Response: &smithyhttp.Response{Response: &http.Response{StatusCode: status}},
			Err:      &smithy.GenericAPIError{Code: code, Message: "no", Fault: fault},
		},
	}
}

func TestFail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		err           error
		wantPermanent bool
	}{
		{name: "a service that does not exist", err: apiError("ServiceNotFoundException", smithy.FaultClient, http.StatusBadRequest), wantPermanent: true},
		{name: "a policy that refuses", err: apiError("AccessDeniedException", smithy.FaultClient, http.StatusForbidden), wantPermanent: true},
		{name: "a parameter AWS rejects", err: apiError("InvalidParameterException", smithy.FaultClient, http.StatusBadRequest), wantPermanent: true},
		{name: "a refusal that names no fault, with a 4xx status", err: apiError("AccessDenied", smithy.FaultUnknown, http.StatusForbidden), wantPermanent: true},
		{name: "an error that names no fault, with a 5xx status", err: apiError("Oops", smithy.FaultUnknown, http.StatusInternalServerError)},
		{name: "a rate limit with a throttle code", err: apiError("ThrottlingException", smithy.FaultClient, http.StatusBadRequest)},
		{name: "a rate limit with status 429", err: apiError("SomeLimit", smithy.FaultClient, http.StatusTooManyRequests)},
		{name: "a request that timed out", err: apiError("RequestTimeout", smithy.FaultClient, http.StatusBadRequest)},
		{name: "expired credentials", err: apiError("ExpiredTokenException", smithy.FaultClient, http.StatusForbidden)},
		{name: "a server error", err: apiError("ServerException", smithy.FaultServer, http.StatusInternalServerError)},
		{name: "a failure to reach AWS", err: errors.New("connection reset")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := Fail(fmt.Errorf("describe: %w", tt.err))
			require.ErrorIs(t, got, tt.err)
			require.Equal(t, tt.wantPermanent, errors.Is(got, action.ErrPermanent))
		})
	}
}

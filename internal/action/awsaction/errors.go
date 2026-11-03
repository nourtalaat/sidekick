package awsaction

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/smithy-go"

	"github.com/nourtalaat/sidekick/internal/action"
)

// refreshedCodes are client errors that a retry fixes, because the SDK reads
// fresh credentials or a fresh clock for it.
var refreshedCodes = map[string]bool{
	"ExpiredToken":          true,
	"ExpiredTokenException": true,
	"RequestExpired":        true,
}

// Fail wraps err for an Execute or Undo that returns it. An answer of AWS that
// says the request is wrong, such as a service that does not exist, a policy
// that refuses the call or a parameter AWS rejects, is wrapped with
// action.ErrPermanent, so Sidekick fails the proposal at once. Rate limits,
// server errors, timeouts, expired credentials and failures to reach AWS stay
// as they are, so Sidekick tries again.
func Fail(err error) error {
	apiErr, ok := errors.AsType[smithy.APIError](err)
	if !ok || apiErr.ErrorFault() != smithy.FaultClient || retryable(err, apiErr.ErrorCode()) {
		return fmt.Errorf("call AWS: %w", err)
	}

	return fmt.Errorf("%w: %w", action.ErrPermanent, err)
}

// retryable reports whether the SDK would try the failed call again, or the
// credentials will be read again for it.
func retryable(err error, code string) bool {
	if refreshedCodes[code] {
		return true
	}

	if resp, ok := errors.AsType[*awshttp.ResponseError](err); ok && resp.HTTPStatusCode() == http.StatusTooManyRequests {
		return true
	}

	return retry.IsErrorRetryables(retry.DefaultRetryables).IsErrorRetryable(err) == aws.TrueTernary ||
		retry.IsErrorThrottles(retry.DefaultThrottles).IsErrorThrottle(err) == aws.TrueTernary
}

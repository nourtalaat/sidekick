//go:generate mockgen -source validator.go -destination mocks_validator.go -package validateclient -write_package_comment=false

package validateclient

import "context"

// Validator validates one request. Client, which calls the validator
// sidecar, and the job-mode client in package validatejob both implement it,
// with the same errors: the error of Validate wraps ErrBadRequest, ErrBusy,
// ErrTimeout, ErrUnavailable or ErrTooLarge.
type Validator interface {
	Validate(ctx context.Context, req Request) (Result, error)
}

var _ Validator = (*Client)(nil)

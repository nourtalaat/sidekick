package gitlabcode

import (
	"errors"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

// refusal is an error for something the action will not do. The model reads
// it as a refusal that names the reason, because it wraps action.ErrLimit,
// and Execute and Undo fail for good on it, because it wraps
// action.ErrPermanent. Without the first, Sidekick would tell the model to try
// again later, and it would propose the same change again.
type refusal struct {
	err error
}

// Error implements error.
func (r *refusal) Error() string {
	return r.err.Error()
}

// Unwrap returns the reason, which may wrap a cause.
func (r *refusal) Unwrap() error {
	return r.err
}

// Is makes the refusal match action.ErrLimit and action.ErrPermanent.
func (*refusal) Is(target error) bool {
	return target == action.ErrLimit || target == action.ErrPermanent
}

// refuse returns a refusal with the message, which may use %w for a cause.
func refuse(format string, args ...any) error {
	return &refusal{err: fmt.Errorf(format, args...)}
}

// refusalOf makes an error of the shared reader that says the path cannot be
// changed, such as a directory, a file that is too large or one that is not
// text, a refusal. Any other error is returned as it is.
func refusalOf(err error) error {
	for _, reason := range []error{
		gitlabgit.ErrNotFile, gitlabgit.ErrTooBig, gitlabgit.ErrNotText,
		gitlabgit.ErrUnsupportedHash, gitlabgit.ErrTooManyEntries, gitlabgit.ErrNotBase64,
	} {
		if errors.Is(err, reason) {
			return &refusal{err: err}
		}
	}

	return err
}

package gitchange

import "github.com/nourtalaat/sidekick/internal/action"

// refusal is an error for something an action will not do. Its text is the
// text of the error it wraps.
type refusal struct {
	err error
}

// Refuse returns an error with the text of err that matches action.ErrLimit
// and action.ErrPermanent, and still unwraps to err. The model reads it as a
// refusal that names its reason, and Execute fails for good on it.
func Refuse(err error) error {
	return &refusal{err: err}
}

// Error implements error.
func (r *refusal) Error() string {
	return r.err.Error()
}

// Unwrap returns the reason.
func (r *refusal) Unwrap() error {
	return r.err
}

// Is makes the refusal match action.ErrLimit and action.ErrPermanent.
func (*refusal) Is(target error) bool {
	return target == action.ErrLimit || target == action.ErrPermanent
}

package awsaction

import (
	"fmt"

	"github.com/nourtalaat/sidekick/internal/action"
)

// refusal is an error that says, for the model or a person to read, why the
// action will not do something, and its text is only the reason. It matches
// action.ErrLimit, because Sidekick shows the model only a Snapshot error that
// wraps it as a refusal with its reason, and anything else as a failure to read
// the target, to try again later. ErrLimit counts as permanent, as does
// action.ErrPermanent, which it also matches, so Execute and Undo fail for good.
type refusal struct {
	text string
}

// Error implements error.
func (e *refusal) Error() string {
	return e.text
}

// Is makes the error match action.ErrLimit and action.ErrPermanent.
func (e *refusal) Is(target error) bool {
	return target == action.ErrLimit || target == action.ErrPermanent
}

// Refuse makes a refusal from format and args: an error that says why an AWS
// action will not do something, and matches both action.ErrLimit and
// action.ErrPermanent. Use it for every reason a Snapshot, Validate, Execute or
// Undo says no for good.
func Refuse(format string, args ...any) error {
	return &refusal{text: fmt.Sprintf(format, args...)}
}

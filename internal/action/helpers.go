package action

import (
	"bytes"
	"encoding/json"
	"fmt"

	"github.com/nourtalaat/sidekick/internal/audit"
)

// DecodeParams decodes the parameters of an action into v and refuses keys
// that v does not have. The error wraps ErrParams. Actions use it in
// Validate and Execute, after Sidekick has checked the parameters against
// the schema, because an action must not rely on its caller for its safety.
func DecodeParams(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("%w: %w", ErrParams, err)
	}

	return nil
}

// LimitError returns an error that says what is outside the limits, built
// from format and args, and wraps ErrLimit and cause, so Sidekick fails the
// proposal at once and a caller can tell which limit it was.
func LimitError(cause error, format string, args ...any) error {
	return fmt.Errorf("%s: %w: %w", fmt.Sprintf(format, args...), ErrLimit, cause)
}

// ApprovedByPolicy reports whether the route's read + write policy approved
// the invocation, with no person.
func (i Invocation) ApprovedByPolicy() bool {
	return i.ApprovedBy == audit.ActorPolicy
}

// ApprovalNote returns the line an action puts under the text it posts in a
// ticket or a thread, to say who approved it: an approver, or the route's
// policy. verb is "Posted" or "Sent".
func (i Invocation) ApprovalNote(verb string) string {
	if i.ApprovedByPolicy() {
		return verb + " by Sidekick, approved by the read + write policy of its route."
	}

	return verb + " by Sidekick after an approver approved it."
}

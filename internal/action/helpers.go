package action

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// DecodeParams decodes the parameters of an action into v and refuses keys
// that v does not have. Actions use it in Validate and Execute, after
// Sidekick has checked the parameters against the schema, because an
// action must not rely on its caller for its safety.
func DecodeParams(raw json.RawMessage, v any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()

	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("decode the parameters: %w", err)
	}

	return nil
}

// LimitError returns an error that says what is outside the limits, built
// from format and args, and wraps ErrLimit and cause, so Sidekick fails the
// proposal at once and a caller can tell which limit it was.
func LimitError(cause error, format string, args ...any) error {
	return fmt.Errorf("%s: %w: %w", fmt.Sprintf(format, args...), ErrLimit, cause)
}

package gitlabapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/nourtalaat/sidekick/internal/source"
)

// StatusError is an answer of GitLab that is not a success.
type StatusError struct {
	Code int
	// Message is GitLab's own error message, untrusted.
	Message string
}

// Error returns the status and GitLab's message.
func (e *StatusError) Error() string {
	text := fmt.Sprintf("GitLab answered %d", e.Code)
	if e.Message != "" {
		text += ": " + e.Message
	}

	return text
}

// IsStatus reports whether err is a GitLab answer with the status code.
func IsStatus(err error, code int) bool {
	se, ok := errors.AsType[*StatusError](err)

	return ok && se.Code == code
}

// readStatusError returns the *StatusError of an answer with code, with
// GitLab's message when the body holds one. The message is a string or an
// object, and OAuth style errors use error and error_description.
func readStatusError(code int, body io.Reader) error {
	e := &StatusError{Code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Message          json.RawMessage `json:"message"`
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
	}

	if json.Unmarshal(data, &parsed) != nil {
		return e
	}

	switch {
	case len(parsed.Message) > 0:
		e.Message = source.CutText(textOf(parsed.Message), maxMessageBytes)
	case parsed.ErrorDescription != "":
		e.Message = source.CutText(parsed.ErrorDescription, maxMessageBytes)
	case len(parsed.Error) > 0:
		e.Message = source.CutText(textOf(parsed.Error), maxMessageBytes)
	}

	return e
}

// textOf returns a JSON string as its text, and any other JSON value as
// compact JSON.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return ""
	}

	return compact.String()
}

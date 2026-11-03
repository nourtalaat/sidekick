package argocd

import (
	"encoding/json"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on the text a result keeps, in bytes.
const (
	// maxNameLen bounds a name, revision, status or other short text from
	// Argo CD.
	maxNameLen = 256
)

// decode decodes a tool's input. The guard has checked it against the
// tool's schema already, so a failure means the schema and the input type
// disagree; it still goes back to the model as a tool error.
func decode[T any](input json.RawMessage) (T, error) {
	var in T

	if len(input) == 0 {
		return in, nil
	}

	if err := json.Unmarshal(input, &in); err != nil {
		return in, &source.ToolError{Message: "the input does not decode: " + err.Error()}
	}

	return in, nil
}

// limitOf returns the limit a call gives, or def, within 1 and most.
func limitOf(limit, def, most int) int {
	if limit <= 0 {
		return def
	}

	return min(limit, most)
}

// cut cuts text from Argo CD to at most n bytes.
func cut(s string, n int) string {
	return source.CutText(s, n)
}

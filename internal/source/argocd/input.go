package argocd

import (
	"encoding/json"
	"fmt"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on the text a result keeps, in bytes.
const (
	// maxNameLen bounds a name, revision, status or other short text from
	// Argo CD.
	maxNameLen = 256
	// maxRefLen bounds the application a call names; a longer one is not
	// project/name, and is refused before it is cut or parsed.
	maxRefLen = 512
	// maxURLLen bounds a repository URL.
	maxURLLen = 1 << 10
	// maxMessageLen bounds a message of an operation or a condition.
	maxMessageLen = 2 << 10
)

// applicationInput is the input of the tools that take one application.
type applicationInput struct {
	Application string `json:"application"`
}

// resolve returns the application a call names. It returns a
// *source.ToolError for a call that does not give project/name, or an
// application the config does not allow, and sends nothing.
func (s *Source) resolve(application string) (argo.AppRef, error) {
	if len(application) > maxRefLen {
		return argo.AppRef{}, &source.ToolError{Message: "application must have the form project/name, for example shop/checkout"}
	}

	ref, err := argo.ParseAppRef(application)
	if err != nil {
		return argo.AppRef{}, &source.ToolError{Message: err.Error()}
	}

	if !s.cfg.Applications.Allows(ref) {
		return argo.AppRef{}, &source.ToolError{Message: fmt.Sprintf("application %q is not one Sidekick may read; allowed: %s",
			ref, s.cfg.Applications)}
	}

	return ref, nil
}

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

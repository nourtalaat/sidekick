package backstage

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxNameLen is the longest name or namespace Backstage accepts.
const maxNameLen = 63

// namePattern is the shape of a Backstage entity name or namespace:
// letters and digits, with single -, _ or . between them.
var namePattern = regexp.MustCompile(`^[A-Za-z0-9]+(?:[-_.][A-Za-z0-9]+)*$`)

// Entity kinds the tools read, in the lower case Backstage accepts in
// paths and refs.
const (
	kindComponent = "component"
	kindResource  = "resource"
	kindAPI       = "api"
	kindSystem    = "system"
	kindGroup     = "group"
	kindUser      = "user"
)

// ref names one catalog entity.
type ref struct {
	kind      string
	namespace string
	name      string
}

// String returns the ref in Backstage's form, kind:namespace/name.
func (r ref) String() string {
	return r.kind + ":" + r.namespace + "/" + r.name
}

// parseRef reads a name or entity ref, [kind:][namespace/]name, as a tool
// input gives it. A missing kind is defaultKind and a missing namespace is
// namespace. It returns a *source.ToolError for anything that is not a
// valid ref of one of kinds, so a bad input never reaches Backstage.
func parseRef(input, defaultKind, namespace string, kinds []string) (ref, error) {
	s := strings.TrimSpace(input)
	r := ref{kind: defaultKind, namespace: namespace}

	if kind, rest, ok := strings.Cut(s, ":"); ok {
		r.kind = strings.ToLower(kind)
		s = rest
	}

	if ns, rest, ok := strings.Cut(s, "/"); ok {
		r.namespace = strings.ToLower(ns)
		s = rest
	}

	r.name = s

	switch {
	case !slices.Contains(kinds, r.kind):
		return ref{}, &source.ToolError{Message: fmt.Sprintf("%q: the kind must be one of %s", input, strings.Join(kinds, ", "))}
	case !validName(r.namespace):
		return ref{}, &source.ToolError{Message: fmt.Sprintf("%q: %q is not a valid catalog namespace", input, r.namespace)}
	case !validName(r.name):
		return ref{}, &source.ToolError{Message: fmt.Sprintf("%q is not a catalog name or entity ref such as %s:%s/checkout-api",
			input, defaultKind, namespace)}
	default:
		return r, nil
	}
}

// targetName returns the name and lower case kind in an entity ref from
// Backstage, such as the target of a relation, kind:namespace/name. It
// reports false for a ref that is not in that form.
func targetName(targetRef string) (kind, name string, ok bool) {
	kind, rest, ok := strings.Cut(targetRef, ":")
	if !ok {
		return "", "", false
	}

	_, name, ok = strings.Cut(rest, "/")
	if !ok || !validName(name) {
		return "", "", false
	}

	return strings.ToLower(kind), name, true
}

// validName reports whether s is a valid entity name or namespace.
func validName(s string) bool {
	return len(s) <= maxNameLen && namePattern.MatchString(s)
}

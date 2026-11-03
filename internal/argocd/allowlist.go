package argocd

import (
	"errors"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
)

// refSeparator joins a project and an application name, as in Argo CD's
// RBAC policies.
const refSeparator = "/"

// namePattern is the shape of a project or application name: a Kubernetes
// DNS subdomain name, in lowercase. It holds no '/', '?' or '%', so a name
// can go into a request path without changing what it names.
var namePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)

// globPattern is the shape of an Allowlist entry: project/name, where each
// part may hold path.Match wildcards.
var globPattern = regexp.MustCompile(`^[a-z0-9.*?\[\]^-]+/[a-z0-9.*?\[\]^-]+$`)

// AppRef names an application the way Argo CD RBAC does: project/name.
type AppRef struct {
	Project string
	Name    string
}

// ParseAppRef parses "project/name". It refuses anything else, including
// a bare name and a name with more parts.
func ParseAppRef(s string) (AppRef, error) {
	project, name, ok := strings.Cut(strings.TrimSpace(s), refSeparator)
	if !ok || !namePattern.MatchString(project) || !namePattern.MatchString(name) {
		return AppRef{}, fmt.Errorf("application %q must have the form project/name, for example shop/checkout", s)
	}

	return AppRef{Project: project, Name: name}, nil
}

// String returns "project/name".
func (r AppRef) String() string {
	return r.Project + refSeparator + r.Name
}

// Allowlist is a list of project/name globs. Argo CD names its applications
// that way in RBAC policies.
type Allowlist []string

// Check checks the globs without any network call: there is at least one,
// each has the form project/name with optional wildcards, and none is
// listed twice. Each error starts with key, the config key of the list,
// and the index of the entry.
func (a Allowlist) Check(key string) error {
	if len(a) == 0 {
		return fmt.Errorf("%s: list at least one project/name glob, such as shop/*", key)
	}

	var errs []error

	seen := map[string]bool{}

	for i, glob := range a {
		entry := fmt.Sprintf("%s[%d]", key, i)

		switch {
		case !globPattern.MatchString(glob):
			errs = append(errs, fmt.Errorf("%s: %q must have the form project/name, with optional wildcards", entry, glob))
		case seen[glob]:
			errs = append(errs, fmt.Errorf("%s: %q is listed twice", entry, glob))
		default:
			if _, err := path.Match(glob, ""); err != nil {
				errs = append(errs, fmt.Errorf("%s: invalid glob %q: %w", entry, glob, err))
			}
		}

		seen[glob] = true
	}

	return errors.Join(errs...)
}

// Allows reports whether one of the globs matches the application.
func (a Allowlist) Allows(r AppRef) bool {
	name := r.String()

	for _, glob := range a {
		if ok, err := path.Match(glob, name); err == nil && ok {
			return true
		}
	}

	return false
}

// Projects returns the projects the globs name, sorted, when every glob
// names its project without wildcards. It returns nil otherwise, because
// then any project may match. A list of applications asks Argo CD for these
// projects only, which makes the answer smaller.
func (a Allowlist) Projects() []string {
	var projects []string

	for _, glob := range a {
		project, _, _ := strings.Cut(glob, refSeparator)
		if strings.ContainsAny(project, `*?[\`) {
			return nil
		}

		if !slices.Contains(projects, project) {
			projects = append(projects, project)
		}
	}

	slices.Sort(projects)

	return projects
}

// String returns the globs joined by commas, for messages.
func (a Allowlist) String() string {
	return strings.Join(a, ", ")
}

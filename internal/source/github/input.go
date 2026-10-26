package github

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// refPattern is the shape of a branch, tag or SHA a tool accepts: the
// characters Git allows in ref names that need no escaping in a URL path.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./+-]*$`)

// workflowPattern is the shape of a workflow file name or ID.
var workflowPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// resolve returns the repository a call names, by repo or by service. It
// returns a *source.ToolError for a call that names neither or both, an
// unknown service, or a repository the config does not allow.
func (s *Source) resolve(t target) (repo, error) {
	name := strings.TrimSpace(t.Repo)
	service := strings.TrimSpace(t.Service)

	switch {
	case name == "" && service == "":
		return repo{}, &source.ToolError{Message: "give repo (owner/name) or service"}
	case name != "" && service != "":
		return repo{}, &source.ToolError{Message: "give repo or service, not both"}
	case service != "":
		mapped, ok := s.cfg.ServiceRepos[service]
		if !ok {
			return repo{}, &source.ToolError{Message: fmt.Sprintf("no repository is configured for service %q; known services: %s",
				service, strings.Join(s.services(), ", "))}
		}

		name = mapped
	}

	if !validRepo(name) {
		return repo{}, &source.ToolError{Message: fmt.Sprintf("repo %q must have the form owner/name", name)}
	}

	if !s.cfg.allows(name) {
		return repo{}, &source.ToolError{Message: fmt.Sprintf("repo %q is not one Sidekick may read; allowed: %s",
			name, strings.Join(s.cfg.Repos, ", "))}
	}

	owner, rest, _ := strings.Cut(name, "/")

	return repo{owner: owner, name: rest}, nil
}

// services returns the configured service names, sorted, or "none".
func (s *Source) services() []string {
	if len(s.cfg.ServiceRepos) == 0 {
		return []string{"none"}
	}

	names := make([]string, 0, len(s.cfg.ServiceRepos))
	for name := range s.cfg.ServiceRepos {
		names = append(names, name)
	}

	slices.Sort(names)

	return names
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

// parseTime parses an optional RFC 3339 time. The zero time means none
// was given.
func parseTime(key, value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}

	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &source.ToolError{Message: fmt.Sprintf("%s: %q is not an RFC 3339 time such as 2026-05-04T10:00:00Z", key, value)}
	}

	return t, nil
}

// checkRef checks an optional branch, tag or SHA. It must not hold ".."
// or anything that would change the API path it goes into.
func checkRef(key, ref string) error {
	if ref == "" {
		return nil
	}

	if !refPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") {
		return &source.ToolError{Message: fmt.Sprintf("%s: %q is not a valid branch, tag or commit SHA", key, ref)}
	}

	return nil
}

// cleanPath returns a repository path without slashes at either end; the
// empty path is the repository root. It refuses empty, "." and ".." parts,
// which would change the API path.
func cleanPath(key, p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}

	for part := range strings.SplitSeq(p, "/") {
		if part == "" || dots(part) {
			return "", &source.ToolError{Message: fmt.Sprintf("%s: %q is not a path inside the repository", key, p)}
		}
	}

	return p, nil
}

// limitOf returns the limit a call gives, or defaultLimit, within 1 and
// maxLimit.
func limitOf(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}

	return min(limit, maxLimit)
}

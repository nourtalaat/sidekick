package gitlab

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/gitlabapi"
	"github.com/nourtalaat/sidekick/internal/source"
)

// refPattern is the shape of a branch, tag or SHA a tool accepts: the
// characters Git allows in ref names that need no escaping in a URL path.
var refPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_./+-]*$`)

// wordPattern is the shape of a pipeline source: lower case words joined by
// underscores, such as merge_request_event.
var wordPattern = regexp.MustCompile(`^[a-z][a-z_]{0,63}$`)

// environmentPattern is the shape of an environment name: any text without
// control characters, at most 255 characters.
var environmentPattern = regexp.MustCompile(`^[^\x00-\x1f\x7f]{1,255}$`)

// target names the project of a call: by its full path, or by a service
// that serviceProjects maps to one. Every tool's input embeds it.
type target struct {
	Project string `json:"project"`
	Service string `json:"service"`
}

// resolve returns the full path of the project a call names, by project or
// by service. It returns a *source.ToolError for a call that names neither
// or both, an unknown service, or a project the config does not allow.
func (s *Source) resolve(t target) (string, error) {
	name := strings.TrimSpace(t.Project)
	service := strings.TrimSpace(t.Service)

	switch {
	case name == "" && service == "":
		return "", &source.ToolError{Message: "give project (the full path, such as group/project) or service"}
	case name != "" && service != "":
		return "", &source.ToolError{Message: "give project or service, not both"}
	case service != "":
		mapped, ok := s.cfg.ServiceProjects[service]
		if !ok {
			return "", &source.ToolError{Message: fmt.Sprintf("no project is configured for service %q; known services: %s",
				service, strings.Join(s.services(), ", "))}
		}

		name = mapped
	}

	if !gitlabapi.ValidProject(name) {
		return "", &source.ToolError{Message: fmt.Sprintf("project %q must be a full path such as group/project", name)}
	}

	if !s.cfg.allows(name) {
		return "", &source.ToolError{Message: fmt.Sprintf("project %q is not one Sidekick may read; allowed: %s",
			name, strings.Join(s.cfg.Projects, ", "))}
	}

	return name, nil
}

// services returns the configured service names, sorted, or "none".
func (s *Source) services() []string {
	if len(s.cfg.ServiceProjects) == 0 {
		return []string{"none"}
	}

	names := make([]string, 0, len(s.cfg.ServiceProjects))
	for name := range s.cfg.ServiceProjects {
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
// or anything that would change the API path or query it goes into.
func checkRef(key, ref string) error {
	if ref == "" {
		return nil
	}

	if !refPattern.MatchString(ref) || strings.Contains(ref, "..") || strings.Contains(ref, "//") || strings.HasSuffix(ref, "/") {
		return &source.ToolError{Message: fmt.Sprintf("%s: %q is not a valid branch, tag or commit SHA", key, ref)}
	}

	return nil
}

// cleanPath returns a path inside a project without slashes at either end;
// the empty path is the root of the project. It refuses empty, "." and ".."
// parts, which would change the API path.
func cleanPath(key, p string) (string, error) {
	p = strings.Trim(p, "/")
	if p == "" {
		return "", nil
	}

	for part := range strings.SplitSeq(p, "/") {
		if part == "" || dots(part) {
			return "", &source.ToolError{Message: fmt.Sprintf("%s: %q is not a path inside the project", key, p)}
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

// dots reports whether s is "." or "..".
func dots(s string) bool {
	return s == "." || s == ".."
}

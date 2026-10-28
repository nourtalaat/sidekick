package azure

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxRange is the longest time range one call may read.
const maxRange = 7 * 24 * time.Hour

var (
	// resourcePattern is the shape of a resource ID a tool accepts: a
	// resource in a resource group, with names of the characters Azure
	// allows that need no escaping in a URL path.
	resourcePattern = regexp.MustCompile(
		`(?i)^/subscriptions/([0-9a-f-]{36})/resourceGroups/[-\w.()]{1,90}/providers/[a-z0-9.]+/[-\w.()/]+$`)
	// groupPattern is the shape of a resource group name.
	groupPattern = regexp.MustCompile(`^[-\w.()]{1,89}[-\w()]$`)
)

// window is the time range of a call.
type window struct {
	start time.Time
	end   time.Time
}

// subscription returns the subscription a call names. With one
// subscription in the config, the call may leave it out. A subscription
// the config does not list is a *source.ToolError that names the allowed
// ones.
func (s *Source) subscription(id string) (string, error) {
	id = strings.TrimSpace(id)

	if id == "" {
		if len(s.cfg.Subscriptions) == 1 {
			return s.cfg.Subscriptions[0], nil
		}

		return "", &source.ToolError{Message: "give subscription; allowed: " + strings.Join(s.cfg.Subscriptions, ", ")}
	}

	for _, allowed := range s.cfg.Subscriptions {
		if strings.EqualFold(id, allowed) {
			return allowed, nil
		}
	}

	return "", &source.ToolError{Message: fmt.Sprintf("subscription %q is not one Sidekick may read; allowed: %s",
		source.CutText(id, maxNameLen), strings.Join(s.cfg.Subscriptions, ", "))}
}

// resource checks a resource ID a call gives: it has the shape of one,
// holds no "." or ".." part, and is in a subscription the config lists.
func (s *Source) resource(key, id string) (string, error) {
	id = strings.TrimSuffix(strings.TrimSpace(id), "/")

	m := resourcePattern.FindStringSubmatch(id)
	if m == nil || hasDots(id) || strings.Contains(id, "//") {
		return "", &source.ToolError{Message: fmt.Sprintf("%s: %q is not an Azure resource ID such as "+
			"/subscriptions/<id>/resourceGroups/<group>/providers/Microsoft.Web/sites/<name>", key, source.CutText(id, maxNameLen))}
	}

	if _, err := s.subscription(m[1]); err != nil {
		return "", &source.ToolError{Message: fmt.Sprintf("%s: the resource is in subscription %s, which Sidekick may not read; allowed: %s",
			key, m[1], strings.Join(s.cfg.Subscriptions, ", "))}
	}

	return id, nil
}

// workspace returns the workspace a call names, by name or ID. With one
// workspace in the config, the call may leave it out.
func (s *Source) workspace(name string) (Workspace, error) {
	name = strings.TrimSpace(name)

	if name == "" && len(s.cfg.Workspaces) == 1 {
		return s.cfg.Workspaces[0], nil
	}

	for _, w := range s.cfg.Workspaces {
		if strings.EqualFold(name, w.ID) || strings.EqualFold(name, workspaceName(w.ResourceID)) {
			return w, nil
		}
	}

	names := make([]string, 0, len(s.cfg.Workspaces))
	for _, w := range s.cfg.Workspaces {
		names = append(names, workspaceName(w.ResourceID))
	}

	switch {
	case len(names) == 0:
		return Workspace{}, &source.ToolError{Message: "no Log Analytics workspace is configured for this source"}
	case name == "":
		return Workspace{}, &source.ToolError{Message: "give workspace; allowed: " + strings.Join(names, ", ")}
	}

	return Workspace{}, &source.ToolError{Message: fmt.Sprintf("workspace %q is not one Sidekick may read; allowed: %s",
		source.CutText(name, maxNameLen), strings.Join(names, ", "))}
}

// window returns the time range a call gives: since and until as RFC 3339
// times, until defaulting to now and since to until minus def. A range
// that is empty or longer than maxRange is a *source.ToolError.
func (s *Source) window(since, until string, def time.Duration) (window, error) {
	end := s.now().UTC()

	if until != "" {
		t, err := parseTime("until", until)
		if err != nil {
			return window{}, fmt.Errorf("read until: %w", err)
		}

		end = t
	}

	start := end.Add(-def)

	if since != "" {
		t, err := parseTime("since", since)
		if err != nil {
			return window{}, fmt.Errorf("read since: %w", err)
		}

		start = t
	}

	switch {
	case !start.Before(end):
		return window{}, &source.ToolError{Message: "since must be before until"}
	case end.Sub(start) > maxRange:
		return window{}, &source.ToolError{Message: fmt.Sprintf("the time range is %s, longer than the %s one call may read; move since or until",
			end.Sub(start), maxRange)}
	}

	return window{start: start, end: end}, nil
}

// timespan returns the window as an ISO 8601 interval, start/end.
func (w window) timespan() string {
	return timestamp(w.start) + "/" + timestamp(w.end)
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

// parseTime parses an RFC 3339 time, in UTC.
func parseTime(key, value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return time.Time{}, &source.ToolError{Message: fmt.Sprintf("%s: %q is not an RFC 3339 time such as 2026-05-04T10:00:00Z",
			key, source.CutText(value, maxNameLen))}
	}

	return t.UTC(), nil
}

// checkGroup checks a resource group name, which goes into an API path or
// filter.
func checkGroup(group string) error {
	if !groupPattern.MatchString(group) {
		return &source.ToolError{Message: fmt.Sprintf("resourceGroup: %q is not a resource group name", source.CutText(group, maxNameLen))}
	}

	return nil
}

// hasDots reports whether a path has a "." or ".." part, which would
// change the API path it goes into.
func hasDots(path string) bool {
	for part := range strings.SplitSeq(path, "/") {
		if part == "." || part == ".." {
			return true
		}
	}

	return false
}

// limitOf returns the limit a call gives, or def, within 1 and most.
func limitOf(limit, def, most int) int {
	if limit <= 0 {
		return def
	}

	return min(limit, most)
}

// timestamp formats a time the way the Azure APIs take it.
func timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

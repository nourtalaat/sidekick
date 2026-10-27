package gcp

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxRange is the longest time range one call may read.
const maxRange = 7 * 24 * time.Hour

// window is the time range of a call.
type window struct {
	start time.Time
	end   time.Time
}

// project returns the project a call names. With one project in the
// config, the call may leave it out. A project the config does not list
// is a *source.ToolError that names the allowed ones.
func (s *Source) project(name string) (string, error) {
	name = strings.TrimSpace(name)

	if name == "" {
		if len(s.cfg.Projects) == 1 {
			return s.cfg.Projects[0], nil
		}

		return "", &source.ToolError{Message: "give project; allowed: " + strings.Join(s.cfg.Projects, ", ")}
	}

	if !slices.Contains(s.cfg.Projects, name) {
		return "", &source.ToolError{Message: fmt.Sprintf("project %q is not one Sidekick may read; allowed: %s",
			source.CutText(name, maxNameLen), strings.Join(s.cfg.Projects, ", "))}
	}

	return name, nil
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

// limitOf returns the limit a call gives, or def, within 1 and most.
func limitOf(limit, def, most int) int {
	if limit <= 0 {
		return def
	}

	return min(limit, most)
}

// quote returns s as a string literal of the Logging query language. It
// is only used for values that cannot hold a quote or a backslash.
func quote(s string) string {
	return `"` + s + `"`
}

// timestamp formats a time the way the Google Cloud APIs take it.
func timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

package aws

import (
	"encoding/json"
	"fmt"
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

// target returns the account and Region a call names. With one account or
// one Region in the config, the call may leave it out. An account or
// Region the config does not list is a *source.ToolError that names the
// allowed ones.
func (s *Source) target(accountID, region string) (target, error) {
	ids := make([]string, 0, len(s.cfg.Accounts))
	for _, a := range s.cfg.Accounts {
		ids = append(ids, a.ID)
	}

	id, err := pick("account", accountID, ids)
	if err != nil {
		return target{}, fmt.Errorf("pick the account: %w", err)
	}

	r, err := pick("region", region, s.cfg.Regions)
	if err != nil {
		return target{}, fmt.Errorf("pick the Region: %w", err)
	}

	return target{account: s.accounts[id], region: r}, nil
}

// window returns the time range a call gives: since and until as RFC 3339
// times. With def above zero, until defaults to now and since to until
// minus def; with def zero both are required. A range that is empty or
// longer than maxRange is a *source.ToolError.
func (s *Source) window(since, until string, def time.Duration) (window, error) {
	if def == 0 {
		for _, f := range []struct{ key, value string }{{"since", since}, {"until", until}} {
			if f.value == "" {
				return window{}, &source.ToolError{Message: f.key + ": required; give the time range to read, at most 7 days"}
			}
		}
	}

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

// pick returns the value of key a call gives, which must be one of
// allowed. With one allowed value, the call may leave it out.
func pick(key, value string, allowed []string) (string, error) {
	value = strings.TrimSpace(value)

	if value == "" {
		if len(allowed) == 1 {
			return allowed[0], nil
		}

		return "", &source.ToolError{Message: fmt.Sprintf("give %s; allowed: %s", key, strings.Join(allowed, ", "))}
	}

	for _, a := range allowed {
		if value == a {
			return a, nil
		}
	}

	return "", &source.ToolError{Message: fmt.Sprintf("%s %q is not one Sidekick may read; allowed: %s",
		key, source.CutText(value, maxNameLen), strings.Join(allowed, ", "))}
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

// timestamp formats a time for result texts.
func timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

package prometheus

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/prometheus/promql/parser"

	"github.com/nourtalaat/sidekick/internal/source"
)

// maxRange is the longest time range one call may read.
const maxRange = 7 * 24 * time.Hour

// defaultRange is the time range of a call that gives no since.
const defaultRange = time.Hour

// window is the time range of a call.
type window struct {
	start time.Time
	end   time.Time
}

// window returns the time range a call gives: since and until as RFC 3339
// times, until defaulting to now and since to until minus defaultRange. A
// range that is empty or longer than maxRange is a *source.ToolError.
func (s *Source) window(since, until string) (window, error) {
	end, err := s.at("until", until)
	if err != nil {
		return window{}, fmt.Errorf("read until: %w", err)
	}

	start := end.Add(-defaultRange)

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

// at returns the time a call gives as an RFC 3339 time, or now.
func (s *Source) at(key, value string) (time.Time, error) {
	if value == "" {
		return s.now().UTC(), nil
	}

	return parseTime(key, value)
}

// parseQuery parses a PromQL query with the Prometheus parser, so a query
// that does not parse is a *source.ToolError and is never sent.
func (s *Source) parseQuery(query string) (parser.Expr, error) {
	if strings.TrimSpace(query) == "" {
		return nil, &source.ToolError{Message: "query: required"}
	}

	expr, err := s.parser.ParseExpr(query)
	if err != nil {
		return nil, &source.ToolError{Message: "query: " + source.CutText(err.Error(), maxMessageLen)}
	}

	return expr, nil
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

// rangeStep returns the step of a range query over w, in whole seconds, so
// that each series has at most maxPoints points.
func rangeStep(w window) int {
	return max(int(math.Ceil(w.end.Sub(w.start).Seconds()/(maxPoints-1))), 1)
}

// limitOf returns the limit a call gives, or def, within 1 and most.
func limitOf(limit, def, most int) int {
	if limit <= 0 {
		return def
	}

	return min(limit, most)
}

// timestamp formats a time the way the Prometheus API takes it.
func timestamp(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// seconds formats a step for the Prometheus API.
func seconds(n int) string {
	return strconv.Itoa(n) + "s"
}

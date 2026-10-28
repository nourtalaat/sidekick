package elasticsearch

import (
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits on the input of a call.
const (
	// maxRange is the longest time range one call may read.
	maxRange = 7 * 24 * time.Hour
	// maxQueryLen bounds the query string of a call, in bytes.
	maxQueryLen = 4 << 10
	// maxFields bounds the fields one search returns.
	maxFields = 50
	// maxFieldLen bounds one field name, in bytes.
	maxFieldLen = 256
)

var (
	// indexPattern is the shape of an index name or index pattern the source
	// accepts. It leaves out commas, exclusions, date math, remote clusters
	// and anything that could change the path of a request.
	indexPattern = regexp.MustCompile(`^[a-z0-9.*][a-z0-9._*-]{0,254}$`)
	// fieldPattern is the shape of a field name the source accepts. With
	// wildcards, '*' may appear too.
	fieldPattern         = regexp.MustCompile(`^[A-Za-z0-9_@][A-Za-z0-9_@.-]*$`)
	wildcardFieldPattern = regexp.MustCompile(`^[A-Za-z0-9_@*][A-Za-z0-9_@.*-]*$`)
	// indexClause finds a reference to the _index field in a query string,
	// with the field name's wildcards and escapes allowed, and captures the
	// start of its value.
	indexClause = regexp.MustCompile(`(?:^|[^A-Za-z0-9_@.*?\\])((?:\\?[_*?])(?:\\?[A-Za-z*?])*)\s*:\s*(.?)`)
	// plainValue is a value of an _index clause that names one index or
	// pattern, as a bare term or a quoted string.
	plainValue = regexp.MustCompile(`^(?:"([a-z0-9._*-]+)"|([a-z0-9._*-]+))(?:$|[\s)])`)
)

// window is the time range of a call.
type window struct {
	start time.Time
	end   time.Time
}

// index returns the index pattern a call names. With one pattern in the
// config, the call may leave it out. An index or pattern that no allowed
// pattern covers is a *source.ToolError that names the allowed ones.
func (s *Source) index(name string) (string, error) {
	name = strings.TrimSpace(name)

	if name == "" {
		if len(s.cfg.IndexPatterns) == 1 {
			return s.cfg.IndexPatterns[0], nil
		}

		return "", &source.ToolError{Message: "give index; allowed: " + strings.Join(s.cfg.IndexPatterns, ", ")}
	}

	if !s.allowed(name) {
		return "", &source.ToolError{Message: fmt.Sprintf("index %q is not one Sidekick may read: give one index or pattern that "+
			"one of these covers: %s", source.CutText(name, maxNameLen), strings.Join(s.cfg.IndexPatterns, ", "))}
	}

	return name, nil
}

// allowed reports whether one of the allowed patterns covers name, an index
// or a pattern. The patterns hold only '*' as a wildcard, and a '*' of name
// can only be matched by a '*' of the pattern, so every index that name
// matches is matched by that pattern too.
func (s *Source) allowed(name string) bool {
	if !validIndex(name) {
		return false
	}

	return slices.ContainsFunc(s.cfg.IndexPatterns, func(p string) bool {
		ok, err := path.Match(p, name)

		return err == nil && ok
	})
}

// query checks the query string of a call: its length, and every clause on
// the _index field, which must name one index or pattern that the allowed
// patterns cover. The search already reads only the call's index, so such
// a clause cannot reach other indexes; it is refused so the query says
// what the search reads.
func (s *Source) query(q string) (string, error) {
	q = strings.TrimSpace(q)

	if len(q) > maxQueryLen {
		return "", &source.ToolError{Message: fmt.Sprintf("query: %d bytes is longer than the %d one call may send", len(q), maxQueryLen)}
	}

	for _, m := range indexClause.FindAllStringSubmatchIndex(q, -1) {
		if !namesIndexField(q[m[2]:m[3]]) {
			continue
		}

		v := plainValue.FindStringSubmatch(q[m[4]:])
		if v == nil {
			return "", &source.ToolError{Message: "query: give _index one index name or pattern, as a plain term or a quoted string"}
		}

		name := v[1] + v[2]
		if !s.allowed(name) {
			return "", &source.ToolError{Message: fmt.Sprintf("query: _index:%s is not an index Sidekick may read; allowed: %s",
				name, strings.Join(s.cfg.IndexPatterns, ", "))}
		}
	}

	return q, nil
}

// window returns the time range a call gives: since and until as RFC 3339
// times, until defaulting to now. A missing since, and a range that is
// empty or longer than maxRange, is a *source.ToolError.
func (s *Source) window(since, until string) (window, error) {
	if strings.TrimSpace(since) == "" {
		return window{}, &source.ToolError{Message: "since: required, so the search reads a bounded time range"}
	}

	start, err := parseTime("since", since)
	if err != nil {
		return window{}, fmt.Errorf("read since: %w", err)
	}

	end := s.now().UTC()

	if until != "" {
		end, err = parseTime("until", until)
		if err != nil {
			return window{}, fmt.Errorf("read until: %w", err)
		}
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

// fields returns the fields a search returns: the call's, with the time and
// message fields added, or the config's defaults.
func (s *Source) fields(names []string) ([]string, error) {
	if len(names) == 0 {
		return s.cfg.defaultFields(), nil
	}

	if len(names) > maxFields {
		return nil, &source.ToolError{Message: fmt.Sprintf("fields: give at most %d fields", maxFields)}
	}

	for _, f := range names {
		if err := checkField(f, true); err != nil {
			return nil, fmt.Errorf("fields: %w", err)
		}
	}

	return withTimeAndMessage(s.cfg.timeField(), s.cfg.messageField(), names), nil
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

// checkField checks a field name, which may hold '*' when wildcards is
// true. A bad name is a *source.ToolError.
func checkField(name string, wildcards bool) error {
	pattern := fieldPattern
	if wildcards {
		pattern = wildcardFieldPattern
	}

	if len(name) > maxFieldLen || !pattern.MatchString(name) {
		return &source.ToolError{Message: fmt.Sprintf("%q is not a field name such as service.name", source.CutText(name, maxNameLen))}
	}

	return nil
}

// namesIndexField reports whether the field name of a query clause, with
// its escapes, names the _index field, also through wildcards such as
// _ind*. A bare '*', which searches the document fields, does not.
func namesIndexField(field string) bool {
	field = strings.ReplaceAll(field, `\`, "")
	if field == "*" {
		return false
	}

	ok, err := path.Match(field, "_index")

	return err == nil && ok
}

// validIndex reports whether name has the shape of an index name or
// pattern the source accepts. "." and ".." are refused, so a name can
// never climb out of the request path.
func validIndex(name string) bool {
	return indexPattern.MatchString(name) && name != "." && name != ".."
}

// withTimeAndMessage returns the time and message fields followed by
// fields, each once.
func withTimeAndMessage(timeField, messageField string, fields []string) []string {
	out := []string{timeField}

	for _, f := range append([]string{messageField}, fields...) {
		if !slices.Contains(out, f) {
			out = append(out, f)
		}
	}

	return out
}

// limitOf returns the limit a call gives, or def, within 1 and most.
func limitOf(limit, def, most int) int {
	if limit <= 0 {
		return def
	}

	return min(limit, most)
}

// timestamp formats a time the way the search servers and the links take
// it.
func timestamp(t time.Time) string {
	return t.UTC().Format("2006-01-02T15:04:05.000Z")
}

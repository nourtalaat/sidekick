package mock

import (
	"encoding/json"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"
)

// holds reports whether every operator of m holds for an argument. value is
// the argument decoded from JSON and present says whether the call has it.
// re is m.Regex compiled, and now is the scenario clock.
func (m Matcher) holds(value any, present bool, re *regexp.Regexp, now time.Time) bool {
	if m.Exists != nil && *m.Exists != present {
		return false
	}

	if !present {
		return m.Exists != nil && m.onlyExists()
	}

	checks := []bool{
		m.Equals == nil || equal(value, m.Equals),
		m.Regex == "" || matchRegex(value, re),
		m.Contains == nil || contains(value, m.Contains),
		m.OneOf == nil || slices.ContainsFunc(m.OneOf, func(want any) bool { return equal(value, want) }),
		m.GTE == nil || compareNumber(value, func(n float64) bool { return n >= *m.GTE }),
		m.LTE == nil || compareNumber(value, func(n float64) bool { return n <= *m.LTE }),
		m.Within == 0 || within(value, now, m.Within),
	}

	return !slices.Contains(checks, false)
}

// onlyExists reports whether exists is the only operator set, so a missing
// argument can satisfy the matcher.
func (m Matcher) onlyExists() bool {
	return m.Equals == nil && m.Regex == "" && m.Contains == nil && m.OneOf == nil &&
		m.GTE == nil && m.LTE == nil && m.Within == 0
}

// equal reports whether a JSON value is want, a value from the scenario
// file. want goes through JSON first, so numbers compare as float64 and
// mappings as map[string]any on both sides.
func equal(value, want any) bool {
	normal, ok := normalize(want)

	return ok && reflect.DeepEqual(value, normal)
}

// matchRegex reports whether value is a string that re matches.
func matchRegex(value any, re *regexp.Regexp) bool {
	s, ok := value.(string)

	return ok && re != nil && re.MatchString(s)
}

// contains reports whether value is a string with want in it, when want is
// a string, or a list with want in it.
func contains(value, want any) bool {
	switch v := value.(type) {
	case string:
		w, ok := want.(string)

		return ok && strings.Contains(v, w)
	case []any:
		return slices.ContainsFunc(v, func(item any) bool { return equal(item, want) })
	default:
		return false
	}
}

// compareNumber reports whether value is a number that ok holds for.
func compareNumber(value any, ok func(float64) bool) bool {
	n, isNumber := value.(float64)

	return isNumber && ok(n)
}

// within reports whether value is an RFC 3339 time no further than d from
// now.
func within(value any, now time.Time, d time.Duration) bool {
	s, ok := value.(string)
	if !ok {
		return false
	}

	t, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		return false
	}

	diff := t.Sub(now)

	return diff <= d && diff >= -d
}

// normalize returns v as encoding/json decodes it into an any.
func normalize(v any) (any, bool) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, false
	}

	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, false
	}

	return out, true
}

package mock

import (
	"encoding/json"
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// clock is the scenario clock of the matcher tests.
var clock = time.Date(2026, 5, 4, 10, 20, 0, 0, time.UTC)

func TestMatcherHolds(t *testing.T) {
	t.Parallel()

	yes, no := true, false
	ten, twenty := 10.0, 20.0

	tests := []struct {
		name    string
		matcher Matcher
		// arg is the argument as JSON; empty means the call does not have
		// it.
		arg  string
		want bool
	}{
		{name: "equals a string", matcher: Matcher{Equals: "checkout-api"}, arg: `"checkout-api"`, want: true},
		{name: "equals another string", matcher: Matcher{Equals: "checkout-api"}, arg: `"checkout"`, want: false},
		{name: "equals an int as a float", matcher: Matcher{Equals: 5}, arg: `5.0`, want: true},
		{name: "equals a bool", matcher: Matcher{Equals: true}, arg: `true`, want: true},
		{name: "equals a mapping", matcher: Matcher{Equals: map[string]any{"a": 1}}, arg: `{"a": 1}`, want: true},
		{name: "equals a string, not a number", matcher: Matcher{Equals: "5"}, arg: `5`, want: false},
		{name: "equals a time from YAML", matcher: Matcher{Equals: clock}, arg: `"2026-05-04T10:20:00Z"`, want: true},
		{name: "equals, argument missing", matcher: Matcher{Equals: "x"}, want: false},

		{name: "regex matches part of the string", matcher: Matcher{Regex: "(?i)latency|duration"}, arg: `"p99 Latency"`, want: true},
		{name: "regex does not match", matcher: Matcher{Regex: "^pool$"}, arg: `"pool size"`, want: false},
		{name: "regex on a number", matcher: Matcher{Regex: "5"}, arg: `5`, want: false},

		{name: "contains text", matcher: Matcher{Contains: "timeout"}, arg: `"pool timeout error"`, want: true},
		{name: "contains no such text", matcher: Matcher{Contains: "timeout"}, arg: `"all good"`, want: false},
		{name: "contains a list item", matcher: Matcher{Contains: 2}, arg: `[1, 2, 3]`, want: true},
		{name: "contains no such list item", matcher: Matcher{Contains: "b"}, arg: `["a", "c"]`, want: false},
		{name: "contains on a number", matcher: Matcher{Contains: "1"}, arg: `1`, want: false},

		{name: "oneOf the values", matcher: Matcher{OneOf: []any{"prod", "staging"}}, arg: `"staging"`, want: true},
		{name: "oneOf none of the values", matcher: Matcher{OneOf: []any{"prod", "staging"}}, arg: `"dev"`, want: false},
		{name: "oneOf with numbers", matcher: Matcher{OneOf: []any{1, 2}}, arg: `2`, want: true},

		{name: "exists, present", matcher: Matcher{Exists: &yes}, arg: `"x"`, want: true},
		{name: "exists, present as null", matcher: Matcher{Exists: &yes}, arg: `null`, want: true},
		{name: "exists, missing", matcher: Matcher{Exists: &yes}, want: false},
		{name: "exists false, missing", matcher: Matcher{Exists: &no}, want: true},
		{name: "exists false, present", matcher: Matcher{Exists: &no}, arg: `"x"`, want: false},

		{name: "gte, above", matcher: Matcher{GTE: &ten}, arg: `11`, want: true},
		{name: "gte, equal", matcher: Matcher{GTE: &ten}, arg: `10`, want: true},
		{name: "gte, below", matcher: Matcher{GTE: &ten}, arg: `9.5`, want: false},
		{name: "gte on a string", matcher: Matcher{GTE: &ten}, arg: `"11"`, want: false},
		{name: "lte, below", matcher: Matcher{LTE: &twenty}, arg: `19`, want: true},
		{name: "lte, equal", matcher: Matcher{LTE: &twenty}, arg: `20`, want: true},
		{name: "lte, above", matcher: Matcher{LTE: &twenty}, arg: `21`, want: false},
		{name: "gte and lte, between", matcher: Matcher{GTE: &ten, LTE: &twenty}, arg: `15`, want: true},
		{name: "gte and lte, outside", matcher: Matcher{GTE: &ten, LTE: &twenty}, arg: `25`, want: false},

		{name: "within, before the clock", matcher: Matcher{Within: 2 * time.Hour}, arg: `"2026-05-04T08:30:00Z"`, want: true},
		{name: "within, after the clock", matcher: Matcher{Within: 2 * time.Hour}, arg: `"2026-05-04T12:20:00Z"`, want: true},
		{name: "within, in another time zone", matcher: Matcher{Within: time.Hour}, arg: `"2026-05-04T12:00:00+02:00"`, want: true},
		{name: "within, too early", matcher: Matcher{Within: 2 * time.Hour}, arg: `"2026-05-04T08:19:59Z"`, want: false},
		{name: "within, not a time", matcher: Matcher{Within: time.Hour}, arg: `"yesterday"`, want: false},
		{name: "within, a number", matcher: Matcher{Within: time.Hour}, arg: `1714816800`, want: false},

		{name: "every operator must hold", matcher: Matcher{Regex: "pool", Contains: "size"}, arg: `"pool timeout"`, want: false},
		{name: "every operator holds", matcher: Matcher{Regex: "pool", Contains: "size"}, arg: `"pool size"`, want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.NoError(t, tt.matcher.validate())

			var re *regexp.Regexp
			if tt.matcher.Regex != "" {
				re = regexp.MustCompile(tt.matcher.Regex)
			}

			var value any
			if tt.arg != "" {
				require.NoError(t, json.Unmarshal([]byte(tt.arg), &value))
			}

			require.Equal(t, tt.want, tt.matcher.holds(value, tt.arg != "", re, clock))
		})
	}
}

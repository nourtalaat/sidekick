package mute_test

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/mute"
)

func eq(name, value string) mute.Matcher {
	return mute.Matcher{Name: name, Value: value, IsEqual: true}
}

func TestCheckMatchers(t *testing.T) {
	t.Parallel()

	many := func(n int) []mute.Matcher {
		out := make([]mute.Matcher, n)
		for i := range out {
			out[i] = eq("a", "b")
		}

		return out
	}

	tests := []struct {
		name     string
		matchers []mute.Matcher
		wantErr  string
	}{
		{name: "one equality matcher", matchers: []mute.Matcher{eq("alertname", "HighLatency")}},
		{name: "every kind", matchers: []mute.Matcher{
			eq("a", "b"), {Name: "c", Value: "d"}, {Name: "e", Value: "f.*", IsRegex: true, IsEqual: true}, {Name: "g", Value: "h", IsRegex: true},
		}},
		{name: "the most matchers", matchers: many(mute.MaxMatchers)},
		{name: "one matcher too many", matchers: many(mute.MaxMatchers + 1), wantErr: "from 1 to 10 matchers"},
		{name: "none", wantErr: "from 1 to 10 matchers"},
		{name: "an empty name", matchers: []mute.Matcher{eq("", "b")}, wantErr: "matcher 1: the label name is empty"},
		{name: "an empty value", matchers: []mute.Matcher{eq("a", "")}, wantErr: "matcher 1: the value is empty"},
		{name: "an empty value of a not-equal matcher", matchers: []mute.Matcher{{Name: "a"}}, wantErr: "the value is empty"},
		{name: "the longest name", matchers: []mute.Matcher{eq(strings.Repeat("a", mute.MaxMatcherNameBytes), "b")}},
		{name: "a name too long", matchers: []mute.Matcher{eq(strings.Repeat("a", mute.MaxMatcherNameBytes+1), "b")}, wantErr: "longer than 64 bytes"},
		{name: "the longest value", matchers: []mute.Matcher{eq("a", strings.Repeat("b", mute.MaxMatcherValueBytes))}},
		{name: "a value too long", matchers: []mute.Matcher{eq("a", strings.Repeat("b", mute.MaxMatcherValueBytes+1))}, wantErr: "longer than 200 bytes"},
		{name: "a name that is not a label name", matchers: []mute.Matcher{eq("a-b", "c")}, wantErr: `"a-b" is not a label name`},
		{name: "a name that starts with a digit", matchers: []mute.Matcher{eq("1a", "c")}, wantErr: "is not a label name"},
		{name: "a bad regular expression", matchers: []mute.Matcher{{Name: "a", Value: "(", IsRegex: true, IsEqual: true}}, wantErr: "not a regular expression"},
		{name: "a bad value that is not a regular expression", matchers: []mute.Matcher{eq("a", "(")}},
		{name: "invalid UTF-8", matchers: []mute.Matcher{eq("a", "b\xff")}, wantErr: "not valid UTF-8"},
		{name: "the second matcher is the bad one", matchers: []mute.Matcher{eq("a", "b"), eq("", "b")}, wantErr: "matcher 2:"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := mute.CheckMatchers(tt.matchers)

			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, action.ErrLimit)
			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestSameMatchers(t *testing.T) {
	t.Parallel()

	a := []mute.Matcher{eq("a", "1"), {Name: "b", Value: "2"}, {Name: "c", Value: "3", IsRegex: true, IsEqual: true}}

	tests := []struct {
		name string
		b    []mute.Matcher
		want bool
	}{
		{name: "the same", b: []mute.Matcher{eq("a", "1"), {Name: "b", Value: "2"}, {Name: "c", Value: "3", IsRegex: true, IsEqual: true}}, want: true},
		{name: "another order", b: []mute.Matcher{{Name: "c", Value: "3", IsRegex: true, IsEqual: true}, eq("a", "1"), {Name: "b", Value: "2"}}, want: true},
		{name: "a value differs", b: []mute.Matcher{eq("a", "1"), {Name: "b", Value: "2"}, {Name: "c", Value: "4", IsRegex: true, IsEqual: true}}},
		{name: "a flag differs", b: []mute.Matcher{eq("a", "1"), {Name: "b", Value: "2", IsEqual: true}, {Name: "c", Value: "3", IsRegex: true, IsEqual: true}}},
		{name: "a regex flag differs", b: []mute.Matcher{eq("a", "1"), {Name: "b", Value: "2"}, {Name: "c", Value: "3", IsEqual: true}}},
		{name: "fewer", b: []mute.Matcher{eq("a", "1")}},
		{name: "none", b: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, mute.SameMatchers(a, tt.b))
			require.Equal(t, tt.want, mute.SameMatchers(tt.b, a))
		})
	}

	t.Run("a repeated matcher counts", func(t *testing.T) {
		t.Parallel()

		require.False(t, mute.SameMatchers([]mute.Matcher{eq("a", "1"), eq("a", "1")}, []mute.Matcher{eq("a", "1"), eq("b", "1")}))
	})
}

func TestMatcherExpression(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		m    mute.Matcher
		want string
	}{
		{name: "equal", m: eq("alertname", "HighLatency"), want: `alertname="HighLatency"`},
		{name: "not equal", m: mute.Matcher{Name: "env", Value: "dev"}, want: `env!="dev"`},
		{name: "regex", m: mute.Matcher{Name: "service", Value: "checkout-.*", IsRegex: true, IsEqual: true}, want: `service=~"checkout-.*"`},
		{name: "not regex", m: mute.Matcher{Name: "service", Value: "x|y", IsRegex: true}, want: `service!~"x|y"`},
		{name: "quotes and backslashes", m: eq("a", `say "hi" \o/`), want: `a="say \"hi\" \\o/"`},
		{name: "a line break", m: eq("a", "one\ntwo"), want: `a="one\ntwo"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.m.Expression())
		})
	}
}

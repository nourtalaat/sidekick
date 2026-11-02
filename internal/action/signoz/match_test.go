package signoz

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNameMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{pattern: "checkout errors", name: "checkout errors", want: true},
		{pattern: "checkout errors", name: "checkout errors high", want: false},
		{pattern: "checkout errors", name: "Checkout errors", want: false},
		{pattern: "checkout *", name: "checkout errors high", want: true},
		{pattern: "checkout *", name: "checkout ", want: true},
		{pattern: "checkout *", name: "checkout", want: false},
		{pattern: "* errors", name: "checkout errors", want: true},
		{pattern: "* errors", name: "checkout errors high", want: false},
		{pattern: "*", name: "", want: true},
		{pattern: "*", name: "anything", want: true},
		{pattern: "a*b*c", name: "a1b2c", want: true},
		{pattern: "a*b*c", name: "abc", want: true},
		{pattern: "a*b*c", name: "acb", want: false},
		{pattern: "a*b*c", name: "a1b2c3", want: false},
		{pattern: "ab*ba", name: "aba", want: false},
		{pattern: "ab*ba", name: "abba", want: true},
		{pattern: "*mid*", name: "the middle one", want: true},
		{pattern: "*mid*", name: "the end", want: false},
		{pattern: "a?c", name: "abc", want: false},
		{pattern: "a?c", name: "a?c", want: true},
		{pattern: "a.c", name: "abc", want: false},
		{pattern: "[a]*", name: "[a] x", want: true},
		{pattern: "", name: "", want: true},
		{pattern: "", name: "x", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+" on "+tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, nameMatches(tt.pattern, tt.name))
		})
	}
}

func TestMatchesAnyName(t *testing.T) {
	t.Parallel()

	patterns := []string{"checkout *", "cart"}

	require.True(t, matchesAnyName(patterns, "checkout latency"))
	require.True(t, matchesAnyName(patterns, "cart"))
	require.False(t, matchesAnyName(patterns, "search"))
	require.False(t, matchesAnyName(nil, "cart"))
}

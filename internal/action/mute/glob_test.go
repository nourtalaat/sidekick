package mute

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		value   string
		want    bool
	}{
		{"checkout", "checkout", true},
		{"checkout", "checkout-api", false},
		{"checkout-*", "checkout-api", true},
		{"checkout-*", "checkout-", true},
		{"checkout-*", "checkout", false},
		{"*", "", true},
		{"*", "anything at all/with slashes", true},
		{"*-api", "checkout-api", true},
		{"*-api", "checkout-api-v2", false},
		{"a*b*c", "abc", true},
		{"a*b*c", "a--b--c", true},
		{"a*b*c", "a--c--b", false},
		{"ab*b", "ab", false},
		{"ab*b", "abb", true},
		{"a**b", "ab", true},
		{"team/*", "team/payments", true},
		{"a.c", "abc", false},
		{"a?c", "abc", false},
		{"[ab]", "a", false},
		{"[ab]", "[ab]", true},
		{"", "", true},
		{"", "a", false},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+" on "+tt.value, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, matchGlob(tt.pattern, tt.value))
		})
	}
}

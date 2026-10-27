package safehttp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchesHost(t *testing.T) {
	t.Parallel()

	patterns := []string{"*.internal.example.com", "status.example.com", "10.0.0.5", "not a pattern"}

	tests := []struct {
		name string
		host string
		want bool
	}{
		{name: "an exact name", host: "status.example.com", want: true},
		{name: "a name in another case", host: "Status.Example.com", want: true},
		{name: "a name below a wildcard", host: "api.internal.example.com", want: true},
		{name: "the wildcard's own name", host: "internal.example.com"},
		{name: "an address", host: "10.0.0.5", want: true},
		{name: "another host", host: "example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, MatchesHost(patterns, tt.host))
		})
	}
}

func TestCovers(t *testing.T) {
	t.Parallel()

	global := []string{"*.internal.example.com", "status.example.com"}

	tests := []struct {
		name    string
		pattern string
		want    bool
	}{
		{name: "the same pattern", pattern: "*.internal.example.com", want: true},
		{name: "a narrower wildcard", pattern: "*.eu.internal.example.com", want: true},
		{name: "a name below a wildcard", pattern: "api.internal.example.com", want: true},
		{name: "an exact name", pattern: "status.example.com", want: true},
		{name: "a wider wildcard", pattern: "*.example.com"},
		{name: "a wildcard below an exact name", pattern: "*.status.example.com"},
		{name: "another host", pattern: "other.example.com"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, Covers(global, tt.pattern))
		})
	}
}

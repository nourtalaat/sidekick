package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDidYouMean(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		input      string
		candidates []string
		want       string
	}{
		{name: "one letter missing", input: "slak-thread", candidates: []string{"callback", "slack-thread"}, want: ` (did you mean "slack-thread"?)`},
		{name: "one letter extra", input: "monitor-prr", candidates: []string{"monitor-pr"}, want: ` (did you mean "monitor-pr"?)`},
		{name: "two edits", input: "investgat", candidates: []string{"investigate"}, want: ` (did you mean "investigate"?)`},
		{name: "too far", input: "zzz", candidates: []string{"slack-thread"}, want: ""},
		{name: "no candidates", input: "anything", candidates: nil, want: ""},
		{name: "exact match is not suggested", input: "claude", candidates: []string{"claude"}, want: ""},
		{name: "ties go to the first name in order", input: "ab", candidates: []string{"ac", "aa"}, want: ` (did you mean "aa"?)`},
		{name: "closer candidate wins", input: "clade", candidates: []string{"clad", "claude"}, want: ` (did you mean "clad"?)`},
		{name: "short names do not match everything", input: "a", candidates: []string{"b"}, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, didYouMean(tt.input, tt.candidates))
		})
	}
}

func TestEditDistance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		a, b string
		want int
	}{
		{name: "equal", a: "listen", b: "listen", want: 0},
		{name: "empty and word", a: "", b: "abc", want: 3},
		{name: "word and empty", a: "abc", b: "", want: 3},
		{name: "substitution", a: "kitten", b: "sitten", want: 1},
		{name: "classic example", a: "kitten", b: "sitting", want: 3},
		{name: "multi-byte characters count once", a: "héllo", b: "hello", want: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, editDistance(tt.a, tt.b))
		})
	}
}

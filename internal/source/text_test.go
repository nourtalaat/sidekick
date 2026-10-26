package source

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCutText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    string
		n    int
		want string
	}{
		{name: "short", s: "abc", n: 5, want: "abc"},
		{name: "exact", s: "abc", n: 3, want: "abc"},
		{name: "cut", s: "abcdef", n: 3, want: "abc"},
		{name: "never splits a character", s: "aé", n: 2, want: "a"},
		{name: "zero", s: "abc", n: 0, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, CutText(tt.s, tt.n))
		})
	}
}

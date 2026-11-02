package githubpr

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChangedLines(t *testing.T) {
	t.Parallel()

	base := "a\nb\nc\nd\ne\n"

	tests := []struct {
		name       string
		old        string
		updated    string
		limit      int
		want       int
		wantWithin bool
	}{
		{name: "the same text", old: base, updated: base, limit: 10, want: 0, wantWithin: true},
		{name: "one line changed", old: base, updated: "a\nb\nX\nd\ne\n", limit: 10, want: 2, wantWithin: true},
		{name: "one line added", old: base, updated: "a\nb\nc\nnew\nd\ne\n", limit: 10, want: 1, wantWithin: true},
		{name: "one line removed", old: base, updated: "a\nb\nd\ne\n", limit: 10, want: 1, wantWithin: true},
		{name: "lines moved", old: "a\nb\nc\n", updated: "c\na\nb\n", limit: 10, want: 2, wantWithin: true},
		{name: "a new file", old: "", updated: "a\nb\nc\n", limit: 10, want: 3, wantWithin: true},
		{name: "a file emptied", old: "a\nb\n", updated: "", limit: 10, want: 2, wantWithin: true},
		{name: "only the final newline differs", old: "a\nb", updated: "a\nb\n", limit: 10, want: 1, wantWithin: true},
		{name: "a blank line added", old: "a\nb\n", updated: "a\n\nb\n", limit: 10, want: 1, wantWithin: true},
		{name: "exactly at the limit", old: base, updated: "a\nb\nX\nd\ne\n", limit: 2, want: 2, wantWithin: true},
		{name: "one over the limit", old: base, updated: "a\nb\nX\nd\ne\n", limit: 1, want: 2},
		{name: "a rewrite far over the limit", old: strings.Repeat("a\n", 500), updated: strings.Repeat("b\n", 500), limit: 40, want: 41},
		{name: "a new file over the limit", old: "", updated: strings.Repeat("x\n", 50), limit: 40, want: 41},
		{name: "everything in common but the ends", old: "start\n" + strings.Repeat("m\n", 2000) + "end\n", updated: "begin\n" + strings.Repeat("m\n", 2000) + "finish\n", limit: 10, want: 4, wantWithin: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, within := changedLines(tt.old, tt.updated, tt.limit)
			require.Equal(t, tt.wantWithin, within)

			if within {
				require.Equal(t, tt.want, got)
			} else {
				require.Greater(t, got, tt.limit)
			}
		})
	}
}

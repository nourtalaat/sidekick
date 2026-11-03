package gitchange

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// render writes lines as the tests read them: the sign, the old and the new
// number (a dot for none) and the text.
func render(lines []Line) []string {
	sign := map[string]string{LineContext: " ", LineAdded: "+", LineRemoved: "-"}
	number := func(n int) string {
		if n == 0 {
			return "."
		}

		return fmt.Sprint(n)
	}

	out := make([]string, 0, len(lines))
	for _, l := range lines {
		out = append(out, fmt.Sprintf("%s%s/%s %s", sign[l.Kind], number(l.Old), number(l.New), l.Text))
	}

	return out
}

func TestLineDiff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		old          string
		updated      string
		want         []string
		finalNewline string
	}{
		{name: "no change", old: "a\nb\n", updated: "a\nb\n", want: []string{" 1/1 a", " 2/2 b"}},
		{name: "an edit at the start", old: "a\nb\nc\n", updated: "x\nb\nc\n", want: []string{"-1/. a", "+./1 x", " 2/2 b", " 3/3 c"}},
		{name: "an edit in the middle", old: "a\nb\nc\n", updated: "a\nx\nc\n", want: []string{" 1/1 a", "-2/. b", "+./2 x", " 3/3 c"}},
		{name: "an edit at the end", old: "a\nb\nc\n", updated: "a\nb\nx\n", want: []string{" 1/1 a", " 2/2 b", "-3/. c", "+./3 x"}},
		{name: "a line inserted", old: "a\nc\n", updated: "a\nb\nc\n", want: []string{" 1/1 a", "+./2 b", " 2/3 c"}},
		{name: "a line deleted", old: "a\nb\nc\n", updated: "a\nc\n", want: []string{" 1/1 a", "-2/. b", " 3/2 c"}},
		{name: "lines that move up", old: "a\nb\nc\nd\n", updated: "b\nc\nd\ne\n", want: []string{"-1/. a", " 2/1 b", " 3/2 c", " 4/3 d", "+./4 e"}},
		{
			name: "two separate edits", old: "a\nb\nc\nd\ne\n", updated: "a\nB\nc\nD\ne\n",
			want: []string{" 1/1 a", "-2/. b", "+./2 B", " 3/3 c", "-4/. d", "+./4 D", " 5/5 e"},
		},
		{name: "removed lines come before added ones", old: "a\nb\nc\n", updated: "x\ny\nc\n", want: []string{"-1/. a", "-2/. b", "+./1 x", "+./2 y", " 3/3 c"}},
		{name: "a new file", old: "", updated: "a\nb\n", want: []string{"+./1 a", "+./2 b"}},
		{name: "a deleted file", old: "a\nb\n", updated: "", want: []string{"-1/. a", "-2/. b"}},
		{name: "a new empty line", old: "", updated: "\n", want: []string{"+./1 "}},
		{name: "only the final newline added", old: "a\nb", updated: "a\nb\n", want: []string{" 1/1 a", "-2/. b", "+./2 b"}, finalNewline: NewlineAdded},
		{name: "only the final newline removed", old: "a\nb\n", updated: "a\nb", want: []string{" 1/1 a", "-2/. b", "+./2 b"}, finalNewline: NewlineRemoved},
		{name: "the final newline removed with another edit", old: "a\nb\n", updated: "a\nc", want: []string{" 1/1 a", "-2/. b", "+./2 c"}, finalNewline: NewlineRemoved},
		{name: "neither text has a final newline", old: "a\nb", updated: "a\nc", want: []string{" 1/1 a", "-2/. b", "+./2 c"}},
		{name: "Windows line ends on both sides", old: "a\r\nb\r\n", updated: "a\r\nc\r\n", want: []string{" 1/1 a\r", "-2/. b\r", "+./2 c\r"}},
		{name: "Windows line ends put in", old: "a\nb\n", updated: "a\r\nb\r\n", want: []string{"-1/. a", "-2/. b", "+./1 a\r", "+./2 b\r"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := LineDiff(tt.old, tt.updated, 100)
			require.True(t, ok)
			require.Equal(t, tt.want, render(got.Lines))
			require.Equal(t, tt.finalNewline, got.FinalNewline)

			added, removed, within := LineCounts(tt.old, tt.updated, 100)
			require.True(t, within)
			require.Equal(t, added, countOf(got.Lines, LineAdded), "the lines agree with the counts of the snapshot")
			require.Equal(t, removed, countOf(got.Lines, LineRemoved), "the lines agree with the counts of the snapshot")

			require.Equal(t, splitLines(tt.old), textsOf(got.Lines, LineAdded), "the old lines are the context and removed lines")
			require.Equal(t, splitLines(tt.updated), textsOf(got.Lines, LineRemoved), "the new lines are the context and added lines")
		})
	}
}

// countOf counts the lines of a kind.
func countOf(lines []Line, kind string) int {
	n := 0

	for _, l := range lines {
		if l.Kind == kind {
			n++
		}
	}

	return n
}

// textsOf returns the text of the lines that are not of kind skip.
func textsOf(lines []Line, skip string) []string {
	var out []string

	for _, l := range lines {
		if l.Kind != skip {
			out = append(out, l.Text)
		}
	}

	return out
}

func TestLineDiffLimit(t *testing.T) {
	t.Parallel()

	old := strings.Repeat("a\n", 3) + "x\n"
	updated := strings.Repeat("a\n", 3) + "y\nz\n"

	tests := []struct {
		name    string
		old     string
		updated string
		limit   int
		ok      bool
	}{
		{name: "at the limit", old: old, updated: updated, limit: 3, ok: true},
		{name: "over the limit", old: old, updated: updated, limit: 2, ok: false},
		{name: "a new file over the limit", old: "", updated: "a\nb\nc\n", limit: 2, ok: false},
		{name: "a deleted file at the limit", old: "a\nb\nc\n", updated: "", limit: 3, ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, ok := LineDiff(tt.old, tt.updated, tt.limit)
			require.Equal(t, tt.ok, ok)
		})
	}
}

func TestDiffHunks(t *testing.T) {
	t.Parallel()

	old := "1\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\n12\n"

	tests := []struct {
		name    string
		updated string
		context int
		want    []string
	}{
		{
			name: "one change with its context", updated: "1\n2\n3\n4\nX\n6\n7\n8\n9\n10\n11\n12\n", context: 2,
			want: []string{" 3/3 3", " 4/4 4", "-5/. 5", "+./5 X", " 6/6 6", " 7/7 7"},
		},
		{
			name: "two changes whose context meets share it", updated: "1\n2\n3\n4\nX\n6\n7\nY\n9\n10\n11\n12\n", context: 2,
			want: []string{" 3/3 3", " 4/4 4", "-5/. 5", "+./5 X", " 6/6 6", " 7/7 7", "-8/. 8", "+./8 Y", " 9/9 9", " 10/10 10"},
		},
		{
			name: "far changes leave the lines between out", updated: "X\n2\n3\n4\n5\n6\n7\n8\n9\n10\n11\nY\n", context: 1,
			want: []string{"-1/. 1", "+./1 X", " 2/2 2", " 11/11 11", "-12/. 12", "+./12 Y"},
		},
		{name: "no change has no hunk", updated: old, context: 3, want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			d, ok := LineDiff(old, tt.updated, 100)
			require.True(t, ok)

			got := render(d.Hunks(tt.context))
			if tt.want == nil {
				require.Empty(t, got)

				return
			}

			require.Equal(t, tt.want, got)
		})
	}
}

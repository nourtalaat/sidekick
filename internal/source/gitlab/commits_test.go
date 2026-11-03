package gitlab

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCountLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		diff        string
		wantAdded   int
		wantRemoved int
	}{
		{name: "an empty diff", diff: ""},
		{name: "one change", diff: "@@ -1,2 +1,2 @@\n a\n-b\n+c\n", wantAdded: 1, wantRemoved: 1},
		{name: "a removed line that starts with dashes", diff: "@@ -1 +0,0 @@\n--- a yaml document\n", wantRemoved: 1},
		{name: "an added line that starts with plus signs", diff: "@@ -0,0 +1 @@\n+++ b\n", wantAdded: 1},
		{name: "the no newline marker is not a change", diff: "@@ -1 +1 @@\n-a\n\\ No newline at end of file\n+b\n", wantAdded: 1, wantRemoved: 1},
		{name: "only context", diff: "@@ -1,2 +1,2 @@\n a\n b\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			added, removed := countLines(tt.diff)
			require.Equal(t, tt.wantAdded, added)
			require.Equal(t, tt.wantRemoved, removed)
		})
	}
}

func TestChangedFiles(t *testing.T) {
	t.Parallel()

	const hunk = "@@ -0,0 +1,400 @@\n"

	line := "+" + strings.Repeat("x", 99) + "\n"
	big := hunk + strings.Repeat(line, 400)

	tests := []struct {
		name        string
		diffs       []apiDiff
		withPatches bool
		want        []fileChange
		wantCut     bool
	}{
		{
			name: "kinds of change",
			diffs: []apiDiff{
				{OldPath: "a", NewPath: "a", Diff: "@@ -1 +1 @@\n-x\n+y\n"},
				{OldPath: "b", NewPath: "b", NewFile: true},
				{OldPath: "c", NewPath: "c", DeletedFile: true},
				{OldPath: "d", NewPath: "e", RenamedFile: true},
			},
			want: []fileChange{
				{Filename: "a", Status: "modified", Additions: 1, Deletions: 1},
				{Filename: "b", Status: "added"},
				{Filename: "c", Status: "removed"},
				{Filename: "e", PreviousFilename: "d", Status: "renamed"},
			},
		},
		{
			name:        "patches are kept while they fit",
			diffs:       []apiDiff{{OldPath: "a", NewPath: "a", Diff: "@@ -1 +1 @@\n-x\n+y\n"}},
			withPatches: true,
			want:        []fileChange{{Filename: "a", Status: "modified", Additions: 1, Deletions: 1, Patch: "@@ -1 +1 @@\n-x\n+y\n"}},
		},
		{
			name:        "patches share one budget, and counts come from the whole diff",
			diffs:       []apiDiff{{NewPath: "a", Diff: big}, {NewPath: "b", Diff: big}, {NewPath: "c", Diff: big}},
			withPatches: true,
			want: []fileChange{
				{Filename: "a", Status: "modified", Additions: 400, Patch: big},
				{Filename: "b", Status: "modified", Additions: 400, Patch: big[:maxPatchBytes-len(big)], PatchCut: true},
				{Filename: "c", Status: "modified", Additions: 400, PatchCut: true},
			},
			wantCut: true,
		},
		{
			name:  "diffs GitLab leaves out say why",
			diffs: []apiDiff{{NewPath: "a", TooLarge: true}, {NewPath: "b", Collapsed: true}},
			want: []fileChange{
				{Filename: "a", Status: "modified", Omitted: omittedTooLarge},
				{Filename: "b", Status: "modified", Omitted: omittedCollapsed},
			},
			wantCut: true,
		},
		{
			name:  "no patches without withPatches",
			diffs: []apiDiff{{NewPath: "a", Diff: "@@ -1 +1 @@\n-x\n+y\n"}},
			want:  []fileChange{{Filename: "a", Status: "modified", Additions: 1, Deletions: 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, cut := changedFiles(tt.diffs, tt.withPatches)
			require.Equal(t, tt.want, got)
			require.Equal(t, tt.wantCut, cut)
		})
	}
}

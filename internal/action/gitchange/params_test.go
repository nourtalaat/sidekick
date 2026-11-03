package gitchange

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

func text(s string) *string {
	return &s
}

func TestPlan(t *testing.T) {
	t.Parallel()

	bounds := Bounds{MaxFiles: 2, MaxChangedLines: 10, BaseBranch: "main"}
	anyFile := func(string) (validateclient.Format, error) { return "", nil }
	onlySrc := func(p string) (validateclient.Format, error) {
		return "", CheckCodePath(p, []string{"src/**"}, nil)
	}

	tests := []struct {
		name     string
		edit     Edit
		formatOf func(string) (validateclient.Format, error)
		want     []PlannedFile
		wantErr  string
	}{
		{
			name:     "a file and a delete",
			edit:     Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "a.txt", Content: text("a")}, {Path: "b.txt", Delete: true}}},
			formatOf: anyFile,
			want:     []PlannedFile{{Path: "a.txt", Content: text("a")}, {Path: "b.txt"}},
		},
		{name: "another base branch", edit: Edit{BaseBranch: "dev", Title: "t", Files: []FileChange{{Path: "a", Delete: true}}}, formatOf: anyFile, wantErr: "is not the allowed base branch"},
		{name: "no files", edit: Edit{BaseBranch: "main", Title: "t"}, formatOf: anyFile, wantErr: "give at least one file"},
		{name: "too many files", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "a", Delete: true}, {Path: "b", Delete: true}, {Path: "c", Delete: true}}}, formatOf: anyFile, wantErr: "3 files"},
		{name: "an empty title", edit: Edit{BaseBranch: "main", Title: " ", Files: []FileChange{{Path: "a", Delete: true}}}, formatOf: anyFile, wantErr: "title: required"},
		{name: "a title over the limit", edit: Edit{BaseBranch: "main", Title: strings.Repeat("t", MaxTitleChars+1), Files: []FileChange{{Path: "a", Delete: true}}}, formatOf: anyFile, wantErr: "title: longer than"},
		{name: "a body over the limit", edit: Edit{BaseBranch: "main", Title: "t", Body: strings.Repeat("b", maxBodyChars+1), Files: []FileChange{{Path: "a", Delete: true}}}, formatOf: anyFile, wantErr: "body: longer than"},
		{name: "a path outside the root", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "../a", Delete: true}}}, formatOf: anyFile, wantErr: "not a clean path"},
		{name: "a path in .git in other case", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "x/.GIT/config", Delete: true}}}, formatOf: anyFile, wantErr: "not a clean path"},
		{name: "a path too long", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: strings.Repeat("a", maxPathBytes+1), Delete: true}}}, formatOf: anyFile, wantErr: "not a clean path"},
		{name: "a path the host refuses", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: ".github/ci.yml", Delete: true}}}, formatOf: onlySrc, wantErr: "built-in refused path"},
		{name: "a path the host does not allow", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "docs/a.md", Delete: true}}}, formatOf: onlySrc, wantErr: "matches none of the limits.paths globs"},
		{name: "content and delete", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "a", Content: text("a"), Delete: true}}}, formatOf: anyFile, wantErr: "give either content or delete"},
		{name: "content with a NUL", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "a", Content: text("a\x00b")}}}, formatOf: anyFile, wantErr: "valid UTF-8"},
		{name: "a path twice in other case", edit: Edit{BaseBranch: "main", Title: "t", Files: []FileChange{{Path: "a", Delete: true}, {Path: "A", Delete: true}}}, formatOf: anyFile, wantErr: "is given twice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Plan(tt.edit, bounds, tt.formatOf)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestParamsSchemaTarget(t *testing.T) {
	t.Parallel()

	schema, err := tools.ParseInputSchema(ParamsSchema("project", "The project, as group/name."))
	require.NoError(t, err)

	_, err = schema.Check(json.RawMessage(`{"project":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","delete":true}]}`))
	require.NoError(t, err)

	_, err = schema.Check(json.RawMessage(`{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","delete":true}]}`))
	require.Error(t, err, "the target key is the host's own")
	require.Contains(t, string(ParamsSchema("project", "The project, as group/name.")), `"description": "The project, as group/name."`)
}

func TestDecodeParams(t *testing.T) {
	t.Parallel()

	type params struct {
		Project string `json:"project"`
		Edit
	}

	got, err := DecodeParams[params](json.RawMessage(`{"project":"g/p","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","content":"c"}]}`))
	require.NoError(t, err)
	require.Equal(t, params{Project: "g/p", Edit: Edit{BaseBranch: "main", Title: "t", Body: "b", Files: []FileChange{{Path: "p", Content: text("c")}}}}, got)

	_, err = DecodeParams[params](json.RawMessage(`{"project":"g/p","extra":1}`))
	require.ErrorContains(t, err, "unknown field")
}

func TestValidContent(t *testing.T) {
	t.Parallel()

	require.True(t, ValidContent(""))
	require.True(t, ValidContent("groups: []\n"))
	require.False(t, ValidContent("a\xffb"))
	require.False(t, ValidContent("a\x00b"))
}

func TestCheckText(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		title   string
		body    string
		wantErr string
	}{
		{name: "title and body at the limits", title: strings.Repeat("t", MaxTitleChars), body: strings.Repeat("b", maxBodyChars)},
		{name: "an empty body", title: "t"},
		{name: "a blank title", title: " \t", wantErr: "title: required"},
		{name: "a title over the limit", title: strings.Repeat("t", MaxTitleChars+1), wantErr: "title: longer than"},
		{name: "a body over the limit", title: "t", body: strings.Repeat("b", maxBodyChars+1), wantErr: "body: longer than"},
		{name: "a NUL in the body", title: "t", body: "a\x00b", wantErr: "valid UTF-8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckText(tt.title, tt.body)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, action.ErrLimit)

				return
			}

			require.NoError(t, err)
		})
	}
}

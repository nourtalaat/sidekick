package gitlabrevert

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
)

const testSHA = "0123456789abcdef0123456789abcdef01234567"

func TestParamsCheck(t *testing.T) {
	t.Parallel()

	valid := Params{Project: "acme/platform/deploy", BaseBranch: "main", Commit: testSHA, Title: "Revert the new retry loop", Body: "It doubles the load."}

	tests := []struct {
		name    string
		change  func(p *Params)
		wantErr string
	}{
		{name: "a commit"},
		{name: "a merge request", change: func(p *Params) { p.Commit, p.MergeRequest = "", 12 }},
		{name: "both", change: func(p *Params) { p.MergeRequest = 12 }, wantErr: "either commit or mergeRequest"},
		{name: "neither", change: func(p *Params) { p.Commit = "" }, wantErr: "either commit or mergeRequest"},
		{name: "a short SHA", change: func(p *Params) { p.Commit = testSHA[:7] }, wantErr: "full 40-character"},
		{name: "an upper-case SHA", change: func(p *Params) { p.Commit = strings.ToUpper(testSHA) }, wantErr: "full 40-character"},
		{name: "a branch name", change: func(p *Params) { p.Commit = "main" }, wantErr: "full 40-character"},
		{name: "a negative merge request IID", change: func(p *Params) { p.Commit, p.MergeRequest = "", -1 }, wantErr: "must be a merge request IID"},
		{name: "a project that is not a full path", change: func(p *Params) { p.Project = "deploy" }, wantErr: "full project path"},
		{name: "an empty title", change: func(p *Params) { p.Title = "" }, wantErr: "title: required"},
		{name: "a title just over the limit", change: func(p *Params) { p.Title = strings.Repeat("t", gitchange.MaxTitleChars+1) }, wantErr: "title: longer than"},
		{name: "a title at the limit", change: func(p *Params) { p.Title = strings.Repeat("t", gitchange.MaxTitleChars) }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p := valid
			if tt.change != nil {
				tt.change(&p)
			}

			err := p.check()
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestParamsSchema(t *testing.T) {
	t.Parallel()

	schema, err := tools.ParseInputSchema(paramsSchema)
	require.NoError(t, err)

	tests := []struct {
		name  string
		input string
		valid bool
	}{
		{name: "a commit", input: `{"project":"a/b","baseBranch":"main","commit":"` + testSHA + `","title":"t","body":"b"}`, valid: true},
		{name: "a merge request", input: `{"project":"a/b","baseBranch":"main","mergeRequest":3,"title":"t","body":"b"}`, valid: true},
		{name: "both, which Params.check refuses", input: `{"project":"a/b","baseBranch":"main","commit":"` + testSHA + `","mergeRequest":3,"title":"t","body":"b"}`, valid: true},
		{name: "neither, which Params.check refuses", input: `{"project":"a/b","baseBranch":"main","title":"t","body":"b"}`, valid: true},
		{name: "a short SHA", input: `{"project":"a/b","baseBranch":"main","commit":"abc123","title":"t","body":"b"}`},
		{name: "merge request zero", input: `{"project":"a/b","baseBranch":"main","mergeRequest":0,"title":"t","body":"b"}`},
		{name: "a field of merge_request", input: `{"project":"a/b","baseBranch":"main","commit":"` + testSHA + `","title":"t","body":"b","files":[]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := schema.Check(json.RawMessage(tt.input))
			if tt.valid {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)
		})
	}
}

func TestDecodeParams(t *testing.T) {
	t.Parallel()

	got, err := decodeParams(json.RawMessage(`{"project":"a/b","baseBranch":"main","mergeRequest":3,"title":"t","body":"b"}`))
	require.NoError(t, err)
	require.Equal(t, Params{Project: "a/b", BaseBranch: "main", MergeRequest: 3, Title: "t", Body: "b"}, got)

	_, err = decodeParams(json.RawMessage(`{"project":"a/b","draft":false}`))
	require.Error(t, err)
}

package githubpr

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// text returns a pointer to s, for the content of a file change.
func text(s string) *string {
	return &s
}

// validParams returns parameters that pass the limits of validLimits.
func validParams() Params {
	return Params{
		Repo: "acme/monitors",
		Edit: gitchange.Edit{
			BaseBranch: "main",
			Title:      "Raise the checkout latency threshold",
			Body:       "The alert fires on short spikes.",
			Files:      []gitchange.FileChange{{Path: "prometheus/rules/checkout.yaml", Content: text("groups: []\n")}},
		},
	}
}

func TestLimitsPlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(p *Params)
		limits  func(l *Limits)
		wantErr string
		want    []gitchange.PlannedFile
	}{
		{
			name: "one file",
			want: []gitchange.PlannedFile{{Path: "prometheus/rules/checkout.yaml", Format: validateclient.FormatPrometheusRules, Content: text("groups: []\n")}},
		},
		{
			name: "files of every format and a delete",
			change: func(p *Params) {
				p.Files = []gitchange.FileChange{
					{Path: "prometheus/rules/a.yaml", Content: text("a")},
					{Path: "datadog/prod/main.tf", Content: text("b")},
					{Path: "grafana/alerting/g.yaml", Delete: true},
				}
			},
			want: []gitchange.PlannedFile{
				{Path: "prometheus/rules/a.yaml", Format: validateclient.FormatPrometheusRules, Content: text("a")},
				{Path: "datadog/prod/main.tf", Format: validateclient.FormatTerraform, Content: text("b")},
				{Path: "grafana/alerting/g.yaml", Format: validateclient.FormatGrafanaAlertFile},
			},
		},
		{name: "the repository in other case", change: func(p *Params) { p.Repo = "Acme/Monitors" }, want: []gitchange.PlannedFile{{Path: "prometheus/rules/checkout.yaml", Format: validateclient.FormatPrometheusRules, Content: text("groups: []\n")}}},
		{name: "another repository", change: func(p *Params) { p.Repo = "acme/other" }, wantErr: `repo "acme/other" is not one of limits.repos (acme/monitors)`},
		{name: "a repository that only starts the same", change: func(p *Params) { p.Repo = "acme/monitors-old" }, wantErr: "is not one of limits.repos"},
		{name: "another base branch", change: func(p *Params) { p.BaseBranch = "develop" }, wantErr: `baseBranch "develop" is not the allowed base branch "main"`},
		{name: "no title", change: func(p *Params) { p.Title = "  " }, wantErr: "title: required"},
		{name: "a title that is too long", change: func(p *Params) { p.Title = strings.Repeat("t", 121) }, wantErr: "title: longer than 120 characters"},
		{name: "a title of the longest size", change: func(p *Params) { p.Title = strings.Repeat("t", 120) }, want: []gitchange.PlannedFile{{Path: "prometheus/rules/checkout.yaml", Format: validateclient.FormatPrometheusRules, Content: text("groups: []\n")}}},
		{name: "a body that is too long", change: func(p *Params) { p.Body = strings.Repeat("b", 8001) }, wantErr: "body: longer than 8000 characters"},
		{name: "a NUL in the body", change: func(p *Params) { p.Body = "a\x00b" }, wantErr: "valid UTF-8 text without NUL"},
		{name: "no files", change: func(p *Params) { p.Files = nil }, wantErr: "files: give at least one file"},
		{
			name: "more files than the limit",
			change: func(p *Params) {
				p.Files = []gitchange.FileChange{
					{Path: "prometheus/rules/a.yaml", Content: text("a")}, {Path: "prometheus/rules/b.yaml", Content: text("b")},
					{Path: "prometheus/rules/c.yaml", Content: text("c")}, {Path: "prometheus/rules/d.yaml", Content: text("d")},
				}
			},
			wantErr: "4 files, the most one pull request may change is 3",
		},
		{
			name: "as many files as the limit",
			change: func(p *Params) {
				p.Files = []gitchange.FileChange{
					{Path: "prometheus/rules/a.yaml", Content: text("a")}, {Path: "prometheus/rules/b.yaml", Content: text("b")}, {Path: "prometheus/rules/c.yaml", Content: text("c")},
				}
			},
			want: []gitchange.PlannedFile{
				{Path: "prometheus/rules/a.yaml", Format: validateclient.FormatPrometheusRules, Content: text("a")},
				{Path: "prometheus/rules/b.yaml", Format: validateclient.FormatPrometheusRules, Content: text("b")},
				{Path: "prometheus/rules/c.yaml", Format: validateclient.FormatPrometheusRules, Content: text("c")},
			},
		},
		{name: "a path outside every glob", change: func(p *Params) { p.Files[0].Path = "src/main.go" }, wantErr: `path "src/main.go" matches none of the limits.paths globs`},
		{name: "a path that only starts like a glob", change: func(p *Params) { p.Files[0].Path = "prometheus/rules-old/a.yaml" }, wantErr: "matches none of the limits.paths globs"},
		{name: "a path that climbs out", change: func(p *Params) { p.Files[0].Path = "prometheus/rules/../../secrets.yaml" }, wantErr: "not a clean path"},
		{name: "an absolute path", change: func(p *Params) { p.Files[0].Path = "/prometheus/rules/a.yaml" }, wantErr: "not a clean path"},
		{name: "a path with a backslash", change: func(p *Params) { p.Files[0].Path = `prometheus\rules\a.yaml` }, wantErr: "not a clean path"},
		{name: "a path through .git", change: func(p *Params) { p.Files[0].Path = "prometheus/rules/.git/config" }, wantErr: "not a clean path"},
		{name: "a path with a newline", change: func(p *Params) { p.Files[0].Path = "prometheus/rules/a\n.yaml" }, wantErr: "not a clean path"},
		{name: "a path that is not clean", change: func(p *Params) { p.Files[0].Path = "prometheus//rules/a.yaml" }, wantErr: "not a clean path"},
		{name: "an empty path", change: func(p *Params) { p.Files[0].Path = "" }, wantErr: "not a clean path"},
		{
			name: "a path given twice",
			change: func(p *Params) {
				p.Files = []gitchange.FileChange{{Path: "prometheus/rules/a.yaml", Content: text("a")}, {Path: "prometheus/rules/A.yaml", Content: text("b")}}
			},
			wantErr: "is given twice",
		},
		{name: "content and delete", change: func(p *Params) { p.Files[0].Delete = true }, wantErr: "give either content or delete"},
		{name: "neither content nor delete", change: func(p *Params) { p.Files[0].Content = nil }, wantErr: "give either content or delete"},
		{name: "content that is not UTF-8", change: func(p *Params) { p.Files[0].Content = text("a\xffb") }, wantErr: "content must be valid UTF-8"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			p, l := validParams(), validLimits()
			if tt.change != nil {
				tt.change(&p)
			}

			if tt.limits != nil {
				tt.limits(&l)
			}

			got, err := l.plan(p)
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

func TestParamsSchema(t *testing.T) {
	t.Parallel()

	schema, err := tools.ParseInputSchema(paramsSchema)
	require.NoError(t, err)

	tests := []struct {
		name    string
		params  string
		wantErr bool
	}{
		{name: "a file with content", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","content":"c"}]}`},
		{name: "a delete", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","delete":true}]}`},
		{name: "an empty file", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","content":""}]}`},
		{name: "content and delete", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","content":"c","delete":true}]}`, wantErr: true},
		{name: "a file with neither", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p"}]}`, wantErr: true},
		{name: "delete set to false", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","delete":false}]}`, wantErr: true},
		{name: "no files", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[]}`, wantErr: true},
		{name: "a missing base branch", params: `{"repo":"a/b","title":"t","body":"b","files":[{"path":"p","delete":true}]}`, wantErr: true},
		{name: "an extra key", params: `{"repo":"a/b","baseBranch":"main","title":"t","body":"b","draft":false,"files":[{"path":"p","delete":true}]}`, wantErr: true},
		{name: "an empty title", params: `{"repo":"a/b","baseBranch":"main","title":"","body":"b","files":[{"path":"p","delete":true}]}`, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := schema.Check(json.RawMessage(tt.params))
			require.Equal(t, tt.wantErr, err != nil)
		})
	}
}

func TestDecodeParams(t *testing.T) {
	t.Parallel()

	p, err := decodeParams(json.RawMessage(`{"repo":"a/b","baseBranch":"main","title":"t","body":"b","files":[{"path":"p","content":"c"},{"path":"q","delete":true}]}`))
	require.NoError(t, err)
	require.Equal(t, Params{
		Repo: "a/b",
		Edit: gitchange.Edit{
			BaseBranch: "main", Title: "t", Body: "b",
			Files: []gitchange.FileChange{{Path: "p", Content: text("c")}, {Path: "q", Delete: true}},
		},
	}, p)

	_, err = decodeParams(json.RawMessage(`{"repo":"a/b","extra":1}`))
	require.ErrorContains(t, err, "unknown field")
}

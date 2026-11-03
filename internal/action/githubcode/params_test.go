package githubcode

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
	"github.com/nourtalaat/sidekick/internal/investigator/tools"
)

func TestLimitsPlan(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		change    func(p *Params)
		limits    func(l *Limits)
		wantErr   string
		wantLimit bool
	}{
		{name: "one file"},
		{name: "an upper case repository", change: func(p *Params) { p.Repo = "Acme/App" }},
		{name: "a repository that is not allowed", change: func(p *Params) { p.Repo = "acme/other" }, wantErr: "is not one of limits.repos", wantLimit: true},
		{name: "another base branch", change: func(p *Params) { p.BaseBranch = "release" }, wantErr: "is not the allowed base branch", wantLimit: true},
		{name: "no title", change: func(p *Params) { p.Title = " " }, wantErr: "title: required"},
		{name: "no files", change: func(p *Params) { p.Files = nil }, wantErr: "give at least one file"},
		{
			name: "files just inside maxFiles",
			change: func(p *Params) {
				p.Files = []gitchange.FileChange{{Path: "cmd/a.go", Content: new("a")}, {Path: "cmd/b.go", Content: new("b")}, {Path: "cmd/c.go", Content: new("c")}}
			},
		},
		{
			name: "files just outside maxFiles",
			change: func(p *Params) {
				p.Files = []gitchange.FileChange{
					{Path: "cmd/a.go", Content: new("a")}, {Path: "cmd/b.go", Content: new("b")}, {Path: "cmd/c.go", Content: new("c")}, {Path: "cmd/d.go", Content: new("d")},
				}
			},
			wantErr: "the most one pull request may change is 3", wantLimit: true,
		},
		{name: "a path outside limits.paths", change: func(p *Params) { p.Files[0].Path = "docs/guide.md" }, wantErr: "matches none of the limits.paths globs", wantLimit: true},
		{name: "a path that is not clean", change: func(p *Params) { p.Files[0].Path = "internal/../cmd/a.go" }, wantErr: "not a clean path"},
		{name: "a path into .git", change: func(p *Params) { p.Files[0].Path = "internal/.git/config" }, wantErr: "not a clean path"},
		{
			name: "the same path twice",
			change: func(p *Params) {
				p.Files = append(p.Files, gitchange.FileChange{Path: strings.ToUpper(testGoPath[:1]) + testGoPath[1:], Content: new("x")})
			},
			wantErr: "is given twice",
		},
		{name: "content and delete", change: func(p *Params) { p.Files[0].Delete = true }, wantErr: "give either content or delete"},
		{name: "content that is binary", change: func(p *Params) { p.Files[0].Content = new("a\x00b") }, wantErr: "valid UTF-8 text without NUL"},
		{name: "content just inside the size limit", change: func(p *Params) { p.Files[0].Content = new(strings.Repeat("x", githubgit.MaxFileBytes)) }},
		{
			name:    "content just outside the size limit",
			change:  func(p *Params) { p.Files[0].Content = new(strings.Repeat("x", githubgit.MaxFileBytes+1)) },
			wantErr: "is larger than 262144 bytes", wantLimit: true,
		},
		{name: "a delete while deleting is off", change: func(p *Params) { p.Files[0] = gitchange.FileChange{Path: testGoPath, Delete: true} }, wantErr: "limits.allowDelete is false", wantLimit: true},
		{
			name:   "a delete while deleting is on",
			change: func(p *Params) { p.Files[0] = gitchange.FileChange{Path: testGoPath, Delete: true} },
			limits: func(l *Limits) { l.AllowDelete = true },
		},
		{name: "a CI file", change: func(p *Params) { p.Files[0].Path = ".github/workflows/ci.yml" }, limits: func(l *Limits) { l.Paths = []string{"**"} }, wantErr: "built-in refused path", wantLimit: true},
		{name: "a CI file in another case", change: func(p *Params) { p.Files[0].Path = ".GitHub/Workflows/ci.yml" }, limits: func(l *Limits) { l.Paths = []string{"**"} }, wantErr: "built-in refused path", wantLimit: true},
		{name: "a CODEOWNERS file", change: func(p *Params) { p.Files[0].Path = "internal/CODEOWNERS" }, wantErr: "built-in refused path", wantLimit: true},
		{name: "an env file", change: func(p *Params) { p.Files[0].Path = "internal/billing/.env" }, wantErr: "built-in refused path", wantLimit: true},
		{name: "a key file", change: func(p *Params) { p.Files[0].Path = "cmd/tls/server.key" }, wantErr: "built-in refused path", wantLimit: true},
		{name: "a deleted CI file", change: func(p *Params) { p.Files[0] = gitchange.FileChange{Path: ".github/ci.yml", Delete: true} }, limits: func(l *Limits) { l.Paths = []string{"**"}; l.AllowDelete = true }, wantErr: "built-in refused path", wantLimit: true},
		{
			name:    "a path an operator refused",
			change:  func(p *Params) { p.Files[0].Path = "internal/legacy/old.go" },
			limits:  func(l *Limits) { l.RefusedPaths = []string{"internal/legacy/**"} },
			wantErr: `limits.refusedPaths "internal/legacy/**"`, wantLimit: true,
		},
		{
			name:   "a path the refused paths do not touch",
			change: func(p *Params) { p.Files[0].Path = "internal/current/new.go" },
			limits: func(l *Limits) { l.RefusedPaths = []string{"internal/legacy/**"} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limits := validLimits()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			p := validParams()
			if tt.change != nil {
				tt.change(&p)
			}

			got, err := limits.plan(p)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.Equal(t, tt.wantLimit, errors.Is(err, action.ErrLimit))

				return
			}

			require.NoError(t, err)
			require.Len(t, got, len(p.Files))
		})
	}
}

func TestParamsSchema(t *testing.T) {
	t.Parallel()

	_, err := tools.ParseInputSchema(paramsSchema)
	require.NoError(t, err)

	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}

	require.NoError(t, json.Unmarshal(paramsSchema, &schema))
	require.ElementsMatch(t, []string{"repo", "baseBranch", "title", "body", "files"}, schema.Required)
	require.NotContains(t, schema.Properties, "draft", "the pull request is always a draft")
}

func TestDecodeParams(t *testing.T) {
	t.Parallel()

	_, err := decodeParams(json.RawMessage(`{"repo":"acme/app","baseBranch":"main","title":"t","body":"b","files":[],"draft":false}`))
	require.ErrorContains(t, err, "unknown field")

	_, err = decodeParams(json.RawMessage(`{`))
	require.Error(t, err)
}

package gitlabcode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/action/gitlabgit"
)

func TestLimitsPlan(t *testing.T) {
	t.Parallel()

	files := func(fs ...gitchange.FileChange) func(p *Params) {
		return func(p *Params) { p.Files = fs }
	}
	change := func(path string) gitchange.FileChange {
		return gitchange.FileChange{Path: path, Content: text("x\n")}
	}

	tests := []struct {
		name    string
		mutate  func(p *Params)
		limits  func(l *Limits)
		wantErr string
		isLimit bool
	}{
		{name: "an allowed file", mutate: files(change("src/a.go"))},
		{name: "a file in the root that a glob names", mutate: files(change("go.mod"))},
		{name: "three files, the most", mutate: files(change("src/a.go"), change("src/b.go"), change("cmd/c.go"))},
		{name: "four files, one too many", mutate: files(change("src/a.go"), change("src/b.go"), change("cmd/c.go"), change("cmd/d.go")), wantErr: "the most one pull request may change is 3", isLimit: true},
		{name: "a project that is not listed", mutate: func(p *Params) { p.Project = "acme/other"; p.Files = []gitchange.FileChange{change("src/a.go")} }, wantErr: "is not one of limits.projects", isLimit: true},
		{name: "a project in another case", mutate: func(p *Params) { p.Project = "ACME/Platform/API"; p.Files = []gitchange.FileChange{change("src/a.go")} }},
		{name: "another base branch", mutate: func(p *Params) { p.BaseBranch = "develop"; p.Files = []gitchange.FileChange{change("src/a.go")} }, wantErr: "is not the allowed base branch", isLimit: true},
		{name: "a path no glob allows", mutate: files(change("docs/a.md")), wantErr: "matches none of the limits.paths globs", isLimit: true},
		{name: "a CI file, though paths allows it", mutate: files(change(".github/workflows/ci.yml")), wantErr: "CI and build automation", isLimit: true},
		{name: "a GitLab CI file, though paths allows it", mutate: files(change("src/.gitlab-ci.yml")), wantErr: "CI and build automation", isLimit: true},
		{name: "CODEOWNERS, though paths allows it", mutate: files(change("CODEOWNERS")), wantErr: "ownership and review rules", isLimit: true},
		{name: "a .env file, though paths allows it", mutate: files(change("src/.env")), wantErr: "secrets and keys", isLimit: true},
		{name: "a refused path in another case", mutate: files(change("SRC/.ENV")), wantErr: "is refused", isLimit: true},
		{name: "a path the operator refused", mutate: files(change("src/legacy/old.go")), wantErr: "limits.refusedPaths", isLimit: true},
		{name: "a delete when deleting is allowed", mutate: files(gitchange.FileChange{Path: "src/a.go", Delete: true})},
		{
			name: "a delete when deleting is not allowed", mutate: files(gitchange.FileChange{Path: "src/a.go", Delete: true}),
			limits: func(l *Limits) { l.AllowDelete = false }, wantErr: "limits.allowDelete is false", isLimit: true,
		},
		{name: "content of exactly the size limit", mutate: files(gitchange.FileChange{Path: "src/a.go", Content: text(strings.Repeat("x", gitlabgit.MaxFileBytes))})},
		{
			name: "content one byte over the size limit", mutate: files(gitchange.FileChange{Path: "src/a.go", Content: text(strings.Repeat("x", gitlabgit.MaxFileBytes+1))}),
			wantErr: "is larger than", isLimit: true,
		},
		{name: "a path that goes up", mutate: files(change("src/../.github/x.yml")), wantErr: "not a clean path"},
		{name: "a path inside .git", mutate: files(change("src/.git/config")), wantErr: "not a clean path"},
		{name: "the same path twice", mutate: files(change("src/a.go"), change("SRC/A.go")), wantErr: "is given twice"},
		{name: "content and delete", mutate: files(gitchange.FileChange{Path: "src/a.go", Content: text("x"), Delete: true}), wantErr: "give either content or delete"},
		{name: "no files", mutate: files(), wantErr: "give at least one file"},
		{name: "a NUL character", mutate: files(gitchange.FileChange{Path: "src/a.go", Content: text("a\x00b")}), wantErr: "valid UTF-8 text"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limits := validLimits()
			if tt.limits != nil {
				tt.limits(&limits)
			}

			p := validParams()
			tt.mutate(&p)

			_, err := limits.plan(p)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)

			if tt.isLimit {
				require.ErrorIs(t, err, action.ErrLimit)
			}
		})
	}
}

func TestLimitsAllowsProject(t *testing.T) {
	t.Parallel()

	l := validLimits()
	require.True(t, l.allowsProject("acme/platform/api"))
	require.True(t, l.allowsProject("Acme/Platform/Api"))
	require.False(t, l.allowsProject("acme/platform"))
	require.False(t, l.allowsProject(""))
}

func TestDecodeParamsRefusesUnknownFields(t *testing.T) {
	t.Parallel()

	_, err := decodeParams([]byte(`{"project":"acme/platform/api","baseBranch":"main","title":"t","body":"b","files":[],"draft":false}`))
	require.ErrorContains(t, err, "unknown field")
}

func TestParamsSchemaNamesTheProject(t *testing.T) {
	t.Parallel()

	require.Contains(t, string(paramsSchema), `"project"`)
	require.Contains(t, string(paramsSchema), `"required": ["project"`)
}

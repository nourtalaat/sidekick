package gitlabcode

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestSpecTellsTheModelTheLimits(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	spec := b.code.Spec()

	require.Equal(t, "code-mr", spec.Name)
	require.JSONEq(t, string(paramsSchema), string(spec.ParamsSchema))

	for _, want := range []string{
		"draft merge request", "never for a monitor", "Allowed projects: " + testProject, "Base branch: main",
		"At most 3 files and 40 added and removed lines", "Allowed paths: src/**", "A file may be at most 256 KiB",
		"Symlinks, submodules and binary files cannot be changed", "Files may be deleted", "CI and build automation",
		"and src/legacy/**", "Sidekick never merges it",
	} {
		require.Contains(t, spec.Description, want)
	}

	noDelete := newBed(t, func(l *Limits) { l.AllowDelete = false; l.RefusedPaths = nil })
	require.Contains(t, noDelete.code.Spec().Description, "Deleting files is not allowed")
	require.NotContains(t, noDelete.code.Spec().Description, "and src/legacy/**")
}

func TestValidate(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)

	require.NoError(t, b.code.Validate(t.Context(), rawParams(t, codeParams())))

	p := codeParams()
	p.Files = change("CODEOWNERS", "x\n")

	err := b.code.Validate(t.Context(), rawParams(t, p))
	require.ErrorIs(t, err, action.ErrLimit)

	require.Error(t, b.code.Validate(t.Context(), []byte(`{"project":`)))
	require.Empty(t, b.gitlab.Requests(), "Validate makes no call")
}

func TestCheckProposal(t *testing.T) {
	t.Parallel()

	t.Run("a change that passes", func(t *testing.T) {
		t.Parallel()

		b := newBed(t, nil)
		b.serveRepo(repo{testPath: regular(testBefore)})

		require.NoError(t, b.code.CheckProposal(t.Context(), rawParams(t, codeParams())))
		require.Empty(t, writes(b.gitlab))
	})

	tests := []struct {
		name  string
		repo  repo
		files []gitchange.FileChange
		want  string
	}{
		{name: "a symlink", repo: repo{testPath: node{text: "t", mode: "120000"}}, files: change(testPath, testAfter), want: "is a symlink"},
		{name: "a submodule", repo: repo{testPath: node{kind: "commit", mode: "160000"}}, files: change(testPath, testAfter), want: "is a submodule"},
		{name: "an unchanged file", repo: repo{testPath: regular(testAfter)}, files: change(testPath, testAfter), want: "already has this content"},
		{name: "a file that is not text", repo: repo{testPath: regular("a\x00b")}, files: change(testPath, testAfter), want: "is not a UTF-8 text file"},
		{name: "a delete of a missing file", repo: repo{testPath: regular(testBefore)}, files: []gitchange.FileChange{{Path: "src/none.go", Delete: true}}, want: "cannot be deleted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.serveRepo(tt.repo)

			p := validParams()
			p.Files = tt.files

			err := b.code.CheckProposal(t.Context(), rawParams(t, p))
			require.ErrorContains(t, err, tt.want)
			require.ErrorIs(t, err, action.ErrLimit)
		})
	}
}

func TestNewMakesNoCallAndNeedsSecrets(t *testing.T) {
	t.Parallel()

	_, err := newCode("code-mr", Config{TokenSecret: testToken, Limits: validLimits()}, registry.Deps{})
	require.ErrorContains(t, err, "GitLab client")

	secrets := registry.NewMockSecrets(gomock.NewController(t))

	built, err := New(t.Context(), "code-mr", Config{BaseURL: "http://127.0.0.1:1", TokenSecret: testToken, Limits: validLimits()}, registry.Deps{Secrets: secrets})
	require.NoError(t, err, "the token is read by the first call, and nothing is called now")
	require.Equal(t, "code-mr", built.Spec().Name)
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), TypeName)

	yamlText := `
baseURL: https://gitlab.example.com
tokenSecret: {name: code-change, key: token}
limits:
  projects: [acme/platform/api]
  paths: ["src/**"]
  maxFiles: 2
  maxChangedLines: 30
  baseBranch: main
`

	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(yamlText), &node))

	secrets := registry.NewMockSecrets(gomock.NewController(t))

	built, err := registry.Actions.Build(t.Context(), TypeName, "code-mr", node.Content[0], registry.Deps{Secrets: secrets})
	require.NoError(t, err)
	require.Equal(t, "code-mr", built.Spec().Name)

	_, isChecker := built.(action.ProposalChecker)
	require.True(t, isChecker)

	_, isSnapshotter := built.(action.Snapshotter)
	require.True(t, isSnapshotter)

	_, isReader := built.(action.FileReader)
	require.True(t, isReader)

	_, isUndoer := built.(action.Undoer)
	require.True(t, isUndoer)

	plain := strings.Replace(yamlText, "https://gitlab.example.com", "http://gitlab.example.com", 1)
	require.NoError(t, yaml.Unmarshal([]byte(plain), &node))

	_, err = registry.Actions.Check(t.Context(), TypeName, "code-mr", node.Content[0])
	require.ErrorContains(t, err, "must use https")
}

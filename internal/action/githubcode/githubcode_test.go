package githubcode

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		params  json.RawMessage
		wantErr string
		wantIs  error
	}{
		{name: "parameters within the limits", params: rawParams(t, validParams())},
		{name: "not JSON", params: json.RawMessage(`{`), wantErr: "decode the parameters"},
		{name: "an unknown field", params: json.RawMessage(`{"repo":"acme/app","draft":false}`), wantErr: "unknown field"},
		{
			name: "outside the limits",
			params: func() json.RawMessage {
				p := validParams()
				p.Files[0].Path = "docs/guide.md"

				return rawParams(t, p)
			}(),
			wantErr: "matches none of the limits.paths globs",
			wantIs:  action.ErrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			err := b.code.Validate(t.Context(), tt.params)
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.Empty(t, b.github.Requests(), "Validate makes no network call")

				return
			}

			require.ErrorContains(t, err, tt.wantErr)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}

func TestSpec(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		limits func(l *Limits)
		want   string
	}{
		{
			name: "the defaults",
			want: "Opens a draft pull request that changes application code. " +
				"Use it only for a bug in code, never for a monitor, a threshold or an alert rule. " +
				"Read each file you want to change with the GitHub source first, then give the whole new content of each file you change, or delete it. " +
				"Make the smallest change that fixes the cause of the problem, and leave every other file and line as it is. " +
				"Allowed repositories: acme/app. Base branch: main. At most 3 files and 40 added and removed lines in all. " +
				"Allowed paths: internal/**, cmd/**, scripts/**. A file may be at most 256 KiB. " +
				"Symlinks, submodules and binary files cannot be changed. Deleting files is not allowed. " +
				"These paths are never changed, even when an allowed path matches them: CI and build automation, ownership and review rules, Git settings and hooks, secrets and keys. " +
				"The pull request is always a draft. " +
				"Sidekick never merges it, asks for a review or assigns anyone: the repository's own checks run on it and a person reviews it before it can merge.",
		},
		{
			name:   "deletes and refused paths of the operator",
			limits: func(l *Limits) { l.AllowDelete = true; l.RefusedPaths = []string{"internal/legacy/**", "cmd/tool/**"} },
			want: "Files may be deleted. " +
				"These paths are never changed, even when an allowed path matches them: CI and build automation, ownership and review rules, Git settings and hooks, secrets and keys, and internal/legacy/**, cmd/tool/**. ",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := newBed(t, tt.limits).code.Spec()

			require.Equal(t, "code-change", spec.Name)

			if tt.limits == nil {
				require.Equal(t, tt.want, spec.Description)
			} else {
				require.Contains(t, spec.Description, tt.want)
			}

			require.NotContains(t, spec.Description, "\u2014")
			require.JSONEq(t, string(paramsSchema), string(spec.ParamsSchema))
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Actions.Types(), TypeName)

	var node yaml.Node
	require.NoError(t, yaml.Unmarshal([]byte(configSection), &node))

	built, err := registry.Actions.Build(t.Context(), TypeName, "code-change", node.Content[0], registry.Deps{Secrets: secretsWithKey(t)})
	require.NoError(t, err)
	require.Equal(t, "code-change", built.Spec().Name)

	_, isChecker := built.(action.ProposalChecker)
	require.True(t, isChecker)

	reader, isReader := built.(gitchange.CIReader)
	require.True(t, isReader, "it can report the CI of its pull requests")
	require.Equal(t, "github", reader.CIHost())
	require.Equal(t, 2*time.Hour, reader.CIReportTimeout())

	_, isFixer := built.(action.MonitorFixer)
	require.False(t, isFixer, "a code change fixes code, not a noisy monitor")

	bad := strings.Replace(configSection, "maxFiles: 2", "maxFiles: 99", 1)
	require.NoError(t, yaml.Unmarshal([]byte(bad), &node))

	_, err = registry.Actions.Check(t.Context(), TypeName, "code-change", node.Content[0])
	require.ErrorContains(t, err, "limits.maxFiles")
}

func TestCheckProposal(t *testing.T) {
	t.Parallel()

	manyLines := strings.Repeat("line\n", 41)
	fortyLines := strings.Repeat("line\n", 40)

	tests := []struct {
		name    string
		files   repoFiles
		change  func(p *Params)
		limits  func(l *Limits)
		wantErr string
		wantIs  error
	}{
		{name: "a changed file", files: repoFiles{testGoPath: regular(testGoBefore)}},
		{name: "an executable file", files: repoFiles{testGoPath: executable(testGoBefore)}},
		{
			name:  "a new file in a new directory",
			files: repoFiles{"cmd/main.go": regular("package main\n")},
			change: func(p *Params) {
				p.Files = []gitchange.FileChange{{Path: "internal/fresh/new.go", Content: new("package fresh\n")}}
			},
		},
		{
			name:   "a new file just inside maxChangedLines",
			files:  repoFiles{"cmd/main.go": regular("package main\n")},
			change: func(p *Params) { p.Files = []gitchange.FileChange{{Path: "cmd/new.txt", Content: &fortyLines}} },
		},
		{
			name:    "a new file just outside maxChangedLines",
			files:   repoFiles{"cmd/main.go": regular("package main\n")},
			change:  func(p *Params) { p.Files = []gitchange.FileChange{{Path: "cmd/new.txt", Content: &manyLines}} },
			wantErr: "more than the 40 lines", wantIs: action.ErrLimit,
		},
		{
			name:   "a delete of a file that exists",
			files:  repoFiles{testGoPath: regular(testGoBefore)},
			change: func(p *Params) { p.Files = []gitchange.FileChange{{Path: testGoPath, Delete: true}} },
			limits: func(l *Limits) { l.AllowDelete = true },
		},
		{
			name:    "a delete of a file that does not exist",
			files:   repoFiles{"cmd/main.go": regular("package main\n")},
			change:  func(p *Params) { p.Files = []gitchange.FileChange{{Path: testGoPath, Delete: true}} },
			limits:  func(l *Limits) { l.AllowDelete = true },
			wantErr: "does not exist on the base branch", wantIs: action.ErrPermanent,
		},
		{name: "a file sent unchanged", files: repoFiles{testGoPath: regular(testGoAfter)}, wantErr: "already has this content", wantIs: action.ErrPermanent},
		{
			name:    "a symlink",
			files:   repoFiles{testGoPath: {mode: modeSymlink, typ: typeBlob, content: "../other.go"}},
			wantErr: "is a symlink", wantIs: action.ErrPermanent,
		},
		{
			name:    "a submodule",
			files:   repoFiles{testGoPath: {mode: modeSubmodule, typ: typeCommit, content: "abc"}},
			wantErr: "is a submodule", wantIs: action.ErrPermanent,
		},
		{
			name:    "a directory",
			files:   repoFiles{testGoPath + "/inner.go": regular("x")},
			wantErr: "is a directory", wantIs: action.ErrPermanent,
		},
		{
			name:    "a path through a file",
			files:   repoFiles{"internal/billing": regular("x")},
			wantErr: "goes through a path that is a file", wantIs: action.ErrPermanent,
		},
		{
			name:    "a path that differs only in case",
			files:   repoFiles{"internal/billing/Invoice.go": regular(testGoBefore)},
			wantErr: "differs only in case", wantIs: action.ErrPermanent,
		},
		{
			name:    "a file larger than the limit at the base",
			files:   repoFiles{testGoPath: regular(strings.Repeat("x", 256<<10+1))},
			wantErr: "is larger than 262144 bytes", wantIs: action.ErrPermanent,
		},
		{
			name:    "a binary file at the base",
			files:   repoFiles{testGoPath: regular("a\x00b")},
			wantErr: "is not a UTF-8 text file", wantIs: action.ErrPermanent,
		},
		{
			name:    "a directory that GitHub lists in part",
			files:   repoFiles{testGoPath: regular(testGoBefore)},
			wantErr: "lists only in part", wantIs: action.ErrPermanent,
		},
		{
			name:    "a path outside the limits",
			files:   repoFiles{testGoPath: regular(testGoBefore)},
			change:  func(p *Params) { p.Files[0].Path = ".github/workflows/ci.yml" },
			wantErr: "refused", wantIs: action.ErrLimit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, tt.limits)

			if tt.name == "a directory that GitHub lists in part" {
				b.serveBase(tt.files, "internal/billing")
			} else {
				b.serveBase(tt.files)
			}

			p := validParams()
			if tt.change != nil {
				tt.change(&p)
			}

			err := b.code.CheckProposal(t.Context(), rawParams(t, p))
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
			require.ErrorIs(t, err, action.ErrLimit)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
		})
	}
}

func TestCheckProposalReadsOneDirectoryOncePerLevel(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.serveBase(repoFiles{"internal/billing/a.go": regular("a\n"), "internal/billing/b.go": regular("b\n")})

	p := validParams()
	p.Files = []gitchange.FileChange{
		{Path: "internal/billing/a.go", Content: new("a2\n")},
		{Path: "internal/billing/b.go", Content: new("b2\n")},
	}

	require.NoError(t, b.code.CheckProposal(t.Context(), rawParams(t, p)))

	trees := 0

	for _, r := range sent(b.github) {
		if strings.HasPrefix(r, "GET "+repoPath("git/trees/")) {
			trees++
		}
	}

	require.Equal(t, 3, trees, "the root, internal and internal/billing, each read once")
	require.Equal(t, []string{
		"GET " + repoPath("git/ref/heads/main"),
		"GET " + repoPath("git/commits/"+testBaseSHA),
	}, sent(b.github)[:2])
}

func TestCheckProposalGitHubErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		status        int
		wantPermanent bool
	}{
		{name: "a branch that does not exist", status: 404, wantPermanent: true},
		{name: "a token without the right", status: 403, wantPermanent: true},
		{name: "a server error", status: 502},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)
			b.github.Handle("GET", repoPath("git/ref/heads/main"), replyStatus(tt.status, `{"message":"no"}`))

			err := b.code.CheckProposal(t.Context(), rawParams(t, validParams()))
			require.Error(t, err)
			require.Equal(t, tt.wantPermanent, errors.Is(err, action.ErrPermanent))
		})
	}
}

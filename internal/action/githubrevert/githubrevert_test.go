package githubrevert

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/action/actiontest"
	"github.com/nourtalaat/sidekick/internal/action/githubgit"
)

func TestNewMakesNoNetworkCall(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.serve()

	require.Equal(t, "revert", b.rev.Spec().Name)
	require.Equal(t, "github", b.rev.CIHost())
	require.Equal(t, 2*time.Hour, b.rev.CIReportTimeout())
	require.Empty(t, b.w.gh.Requests())
}

func TestValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(p *Params)
		wantErr string
		wantIs  error
	}{
		{name: "a commit"},
		{name: "a pull request", change: func(p *Params) { p.Commit, p.PullRequest = "", 3 }},
		{name: "the repository in other case", change: func(p *Params) { p.Repo = "ACME/API" }},
		{name: "another repository", change: func(p *Params) { p.Repo = "acme/other" }, wantErr: "is not one of limits.repos", wantIs: action.ErrLimit},
		{name: "another base branch", change: func(p *Params) { p.BaseBranch = "dev" }, wantErr: "is not the allowed base branch", wantIs: action.ErrLimit},
		{name: "neither a commit nor a pull request", change: func(p *Params) { p.Commit = "" }, wantErr: "either commit or pullRequest"},
		{name: "a short SHA", change: func(p *Params) { p.Commit = p.Commit[:7] }, wantErr: "full 40-character"},
		{name: "an empty title", change: func(p *Params) { p.Title = "" }, wantErr: "title: required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := newBed(t, nil)

			p := byCommit(testSHA)
			if tt.change != nil {
				tt.change(&p)
			}

			err := b.rev.Validate(t.Context(), marshal(t, p))
			if tt.wantErr == "" {
				require.NoError(t, err)
				require.Empty(t, b.w.gh.Requests(), "Validate makes no network call")

				return
			}

			require.ErrorContains(t, err, tt.wantErr)

			if tt.wantIs != nil {
				require.ErrorIs(t, err, tt.wantIs)
			}
		})
	}

	b := newBed(t, nil)
	require.Error(t, b.rev.Validate(t.Context(), []byte(`{"repo":"acme/api","draft":false}`)), "the parameters have no draft key")
}

func TestSpecTellsTheModelTheLimits(t *testing.T) {
	t.Parallel()

	b := newBed(t, func(l *Limits) { l.MaxAge = 48 * time.Hour })

	spec := b.rev.Spec()
	require.Equal(t, "revert", spec.Name)

	for _, want := range []string{"acme/api", "Base branch: main", "48h0m0s", "at most 5 files", "100 added and removed lines", "full 40-character SHA", "github.code_change"} {
		require.Contains(t, spec.Description, want)
	}

	require.NotContains(t, strings.ToLower(spec.Description), "draft: false")
}

func TestSpecSnapshot(t *testing.T) {
	t.Parallel()

	b := newBed(t, func(l *Limits) {
		l.Repos = []string{
			"quillcart/cart-api", "quillcart/billing-portal", "quillcart/deploy-config",
			"quillcart/order-intake", "quillcart/web-shop", "quillcart/ledger-api",
		}
	})

	actiontest.CheckSpecSnapshot(t, TypeName, b.rev)
}

func TestReadBlob(t *testing.T) {
	t.Parallel()

	b := newBed(t, nil)
	b.w.commit(nil, "Initial", testNow.Add(-time.Hour), map[string]wfile{configPath: txt(configBefore), "logo.png": txt("a\x00b")})
	b.w.serve()

	params := marshal(t, byCommit(testSHA))

	tests := []struct {
		name    string
		params  []byte
		blob    string
		want    string
		wantErr string
		wantIs  error
	}{
		{name: "a text file", params: params, blob: blobOf(configBefore), want: configBefore},
		{name: "a repository the limits do not allow", params: marshal(t, Params{Repo: "acme/other", BaseBranch: "main", Commit: testSHA, Title: "t"}), blob: blobOf(configBefore), wantErr: "is not one of limits.repos", wantIs: action.ErrLimit},
		{name: "not a blob SHA", params: params, blob: "main", wantErr: "is not a blob SHA", wantIs: githubgit.ErrBadBlob},
		{name: "a file that is not text", params: params, blob: blobOf("a\x00b"), wantErr: "is not a UTF-8 text file", wantIs: githubgit.ErrNotText},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := b.rev.ReadBlob(t.Context(), tt.params, tt.blob)
			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)
				require.ErrorIs(t, err, tt.wantIs)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
}

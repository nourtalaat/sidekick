package action

import (
	"path"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/event"
)

func TestRepoLimitsCheck(t *testing.T) {
	t.Parallel()

	limits := RepoLimits{
		Repos:           []string{"acme/monitors"},
		Paths:           []string{"datadog/**", "alerts/*.yaml"},
		MaxFiles:        2,
		MaxChangedLines: 50,
	}

	tests := []struct {
		name         string
		limits       RepoLimits
		repo         string
		files        []FileChange
		wantErr      error
		wantErrMatch string
	}{
		{
			name:   "allowed change",
			limits: limits,
			repo:   "acme/monitors",
			files:  []FileChange{{Path: "datadog/api/latency.json", ChangedLines: 10}, {Path: "alerts/api.yaml", ChangedLines: 40}},
		},
		{name: "no files", limits: limits, repo: "acme/monitors"},
		{
			name:         "repo not allowed",
			limits:       limits,
			repo:         "acme/payments",
			files:        []FileChange{{Path: "datadog/a.json"}},
			wantErr:      ErrLimit,
			wantErrMatch: `repo "acme/payments"`,
		},
		{
			name:         "too many files",
			limits:       limits,
			repo:         "acme/monitors",
			files:        []FileChange{{Path: "datadog/a.json"}, {Path: "datadog/b.json"}, {Path: "datadog/c.json"}},
			wantErr:      ErrLimit,
			wantErrMatch: "3 files, limit 2",
		},
		{
			name:         "path outside the allowed paths",
			limits:       limits,
			repo:         "acme/monitors",
			files:        []FileChange{{Path: "src/main.go"}},
			wantErr:      ErrLimit,
			wantErrMatch: `path "src/main.go"`,
		},
		{
			name:         "double star needs a path below its prefix",
			limits:       limits,
			repo:         "acme/monitors",
			files:        []FileChange{{Path: "datadog"}},
			wantErr:      ErrLimit,
			wantErrMatch: `path "datadog"`,
		},
		{
			name:         "double star prefix must end at a directory",
			limits:       limits,
			repo:         "acme/monitors",
			files:        []FileChange{{Path: "datadog-old/a.json"}},
			wantErr:      ErrLimit,
			wantErrMatch: `path "datadog-old/a.json"`,
		},
		{
			name:         "single star does not cross directories",
			limits:       limits,
			repo:         "acme/monitors",
			files:        []FileChange{{Path: "alerts/team/api.yaml"}},
			wantErr:      ErrLimit,
			wantErrMatch: `path "alerts/team/api.yaml"`,
		},
		{
			name:         "too many changed lines in total",
			limits:       limits,
			repo:         "acme/monitors",
			files:        []FileChange{{Path: "datadog/a.json", ChangedLines: 30}, {Path: "datadog/b.json", ChangedLines: 21}},
			wantErr:      ErrLimit,
			wantErrMatch: "51 changed lines, limit 50",
		},
		{
			name:   "zero limits mean no file or line limit",
			limits: RepoLimits{Repos: []string{"acme/monitors"}, Paths: []string{"**"}},
			repo:   "acme/monitors",
			files:  []FileChange{{Path: "a", ChangedLines: 1_000}, {Path: "b"}, {Path: "c"}},
		},
		{
			name:    "no paths allow nothing",
			limits:  RepoLimits{Repos: []string{"acme/monitors"}},
			repo:    "acme/monitors",
			files:   []FileChange{{Path: "datadog/a.json"}},
			wantErr: ErrLimit,
		},
		{
			name:         "bad path pattern is a config error, not a limit",
			limits:       RepoLimits{Repos: []string{"acme/monitors"}, Paths: []string{"alerts/["}},
			repo:         "acme/monitors",
			files:        []FileChange{{Path: "alerts/a.yaml"}},
			wantErr:      path.ErrBadPattern,
			wantErrMatch: `match path "alerts/a.yaml"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.limits.Check(tt.repo, tt.files)
			if tt.wantErr == nil {
				require.NoError(t, err)

				return
			}

			require.ErrorIs(t, err, tt.wantErr)
			require.ErrorContains(t, err, tt.wantErrMatch)
		})
	}
}

func TestEffectiveMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		route               Mode
		trust               event.Trust
		allowUntrustedWrite bool
		want                Mode
	}{
		{name: "unset mode is read-only", route: "", trust: event.Trusted, want: ReadOnly},
		{name: "unset mode is read-only for untrusted events", route: "", trust: event.Untrusted, want: ReadOnly},
		{name: "read-only stays read-only", route: ReadOnly, trust: event.Trusted, want: ReadOnly},
		{name: "approved stays approved", route: Approved, trust: event.Untrusted, want: Approved},
		{name: "read-write with a trusted sender", route: ReadWrite, trust: event.Trusted, want: ReadWrite},
		{name: "read-write with an untrusted sender drops to approved", route: ReadWrite, trust: event.Untrusted, want: Approved},
		{
			name:                "read-write with an untrusted sender and the override",
			route:               ReadWrite,
			trust:               event.Untrusted,
			allowUntrustedWrite: true,
			want:                ReadWrite,
		},
		{name: "an unknown trust value counts as untrusted", route: ReadWrite, trust: "maybe", want: Approved},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, EffectiveMode(tt.route, tt.trust, tt.allowUntrustedWrite))
		})
	}
}

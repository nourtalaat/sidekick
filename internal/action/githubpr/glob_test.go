package githubpr

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMatchGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		pattern string
		name    string
		want    bool
	}{
		{pattern: "prometheus/rules/**", name: "prometheus/rules/a.yaml", want: true},
		{pattern: "prometheus/rules/**", name: "prometheus/rules/team/deep/a.yaml", want: true},
		{pattern: "prometheus/rules/**", name: "prometheus/rules", want: true},
		{pattern: "prometheus/rules/**", name: "prometheus/rules-old/a.yaml"},
		{pattern: "prometheus/rules/**", name: "prometheus/other/a.yaml"},
		{pattern: "prometheus/*.yaml", name: "prometheus/a.yaml", want: true},
		{pattern: "prometheus/*.yaml", name: "prometheus/team/a.yaml"},
		{pattern: "**/*.tf", name: "main.tf", want: true},
		{pattern: "**/*.tf", name: "datadog/prod/main.tf", want: true},
		{pattern: "**/*.tf", name: "datadog/prod/main.tfvars"},
		{pattern: "datadog/**/monitors.tf", name: "datadog/monitors.tf", want: true},
		{pattern: "datadog/**/monitors.tf", name: "datadog/a/b/monitors.tf", want: true},
		{pattern: "datadog/**/monitors.tf", name: "datadog/a/b/other.tf"},
		{pattern: "alerts/?.yaml", name: "alerts/a.yaml", want: true},
		{pattern: "alerts/?.yaml", name: "alerts/ab.yaml"},
		{pattern: "alerts/[ab].yaml", name: "alerts/b.yaml", want: true},
		{pattern: "alerts/[ab].yaml", name: "alerts/c.yaml"},
		{pattern: "**", name: "anything/at/all", want: true},
		{pattern: "a/b", name: "a/b/c"},
		{pattern: "a/b/c", name: "a/b"},
		{pattern: "alerts/[", name: "alerts/a"},
	}

	for _, tt := range tests {
		t.Run(tt.pattern+" "+tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, matchGlob(tt.pattern, tt.name))
		})
	}
}

func TestCheckGlob(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		pattern string
		wantErr string
	}{
		{name: "a recursive glob", pattern: "prometheus/rules/**"},
		{name: "a glob with a wildcard", pattern: "datadog/*/monitors.tf"},
		{name: "empty", pattern: "", wantErr: "required"},
		{name: "a leading slash", pattern: "/rules/**", wantErr: "without a leading slash"},
		{name: "an empty segment", pattern: "rules//a.yaml", wantErr: "empty path segment"},
		{name: "a trailing slash", pattern: "rules/", wantErr: "empty path segment"},
		{name: "** inside a segment", pattern: "rules/a**", wantErr: "** must be a whole path segment"},
		{name: "a bad bracket", pattern: "rules/[a", wantErr: "not a valid glob"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := checkGlob(tt.pattern)
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

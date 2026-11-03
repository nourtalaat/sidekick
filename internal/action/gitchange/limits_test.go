package gitchange

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

func TestBoundsCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		bounds Bounds
		want   []string
	}{
		{name: "the smallest limits", bounds: Bounds{MaxFiles: 1, MaxChangedLines: 1, BaseBranch: "main"}},
		{name: "the largest limits", bounds: Bounds{MaxFiles: 25, MaxChangedLines: 1000, BaseBranch: "release/2026.11"}},
		{name: "nothing set", want: []string{
			"limits.maxFiles: must be from 1 to 25, got 0",
			"limits.maxChangedLines: must be from 1 to 1000, got 0",
			"limits.baseBranch: required",
		}},
		{name: "just over the largest limits", bounds: Bounds{MaxFiles: 26, MaxChangedLines: 1001, BaseBranch: "main"}, want: []string{
			"limits.maxFiles: must be from 1 to 25, got 26",
			"limits.maxChangedLines: must be from 1 to 1000, got 1001",
		}},
		{name: "a negative limit", bounds: Bounds{MaxFiles: -1, MaxChangedLines: 5, BaseBranch: "main"}, want: []string{
			"limits.maxFiles: must be from 1 to 25, got -1",
		}},
		{name: "a base branch that is not a name", bounds: Bounds{MaxFiles: 1, MaxChangedLines: 1, BaseBranch: "main..x"}, want: []string{
			`limits.baseBranch: "main..x" is not a branch name this action accepts`,
		}},
		{name: "a base branch with a leading hyphen", bounds: Bounds{MaxFiles: 1, MaxChangedLines: 1, BaseBranch: "-main"}, want: []string{
			`limits.baseBranch: "-main" is not a branch name this action accepts`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := tt.bounds.Check()
			got := make([]string, len(errs))

			for i, err := range errs {
				got[i] = err.Error()
			}

			require.Equal(t, strings.Join(tt.want, "\n"), strings.Join(got, "\n"))
		})
	}
}

func TestPathRulesCheck(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rules PathRules
		want  []string
	}{
		{name: "one rule", rules: PathRules{{Glob: "rules/**", Format: validateclient.FormatPrometheusRules}}},
		{name: "no rules", want: []string{`limits.paths: list at least one rule, such as {glob: "prometheus/rules/**", format: prometheus_rules}`}},
		{name: "rules that are wrong", rules: PathRules{{Glob: "", Format: validateclient.FormatTerraform}, {Glob: "a/**"}, {Glob: "b/**", Format: "nagios"}}, want: []string{
			"limits.paths[0].glob: required",
			"limits.paths[1].format: required; use one of prometheus_rules, terraform, grafana_alert_file",
			`limits.paths[2].format: unknown format "nagios"; use one of prometheus_rules, terraform, grafana_alert_file`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			errs := tt.rules.Check()
			got := make([]string, len(errs))

			for i, err := range errs {
				got[i] = err.Error()
			}

			require.Equal(t, strings.Join(tt.want, "\n"), strings.Join(got, "\n"))
		})
	}
}

func TestPathRulesFormatOf(t *testing.T) {
	t.Parallel()

	rules := PathRules{
		{Glob: "tf/special/**", Format: validateclient.FormatGrafanaAlertFile},
		{Glob: "tf/**", Format: validateclient.FormatTerraform},
	}

	format, err := rules.FormatOf("tf/special/a.json")
	require.NoError(t, err)
	require.Equal(t, validateclient.FormatGrafanaAlertFile, format, "the first rule that matches decides")

	format, err = rules.FormatOf("tf/main.tf")
	require.NoError(t, err)
	require.Equal(t, validateclient.FormatTerraform, format)

	_, err = rules.FormatOf("docs/a.md")
	require.ErrorIs(t, err, action.ErrLimit)
	require.ErrorContains(t, err, "matches none of the limits.paths globs")
}

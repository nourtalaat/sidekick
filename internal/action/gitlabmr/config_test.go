package gitlabmr

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// testToken is the secret of the token in the tests.
var testToken = registry.SecretRef{Name: "monitor-mr", Key: "token"}

// validLimits returns limits that pass the config check.
func validLimits() Limits {
	return Limits{
		Projects: []string{"acme/platform/monitors"},
		Paths: []gitchange.PathRule{
			{Glob: "prometheus/rules/**", Format: validateclient.FormatPrometheusRules},
			{Glob: "datadog/**", Format: validateclient.FormatTerraform},
			{Glob: "grafana/alerting/**", Format: validateclient.FormatGrafanaAlertFile},
		},
		Bounds: gitchange.Bounds{MaxFiles: 3, MaxChangedLines: 40, BaseBranch: "main"},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(c *Config)
		want   []string
	}{
		{name: "a whole config"},
		{name: "a base URL for a self-managed GitLab", change: func(c *Config) { c.BaseURL = "https://gitlab.example.com" }},
		{name: "a base URL under a sub-path", change: func(c *Config) { c.BaseURL = "https://example.com/gitlab/" }},
		{name: "plain http on localhost", change: func(c *Config) { c.BaseURL = "http://localhost:8080/" }},
		{name: "nothing set", change: func(c *Config) { *c = Config{} }, want: []string{
			"tokenSecret: required, as { name, key }",
			"limits.projects: list at least one project, as its full path such as acme/platform/monitors",
			`limits.paths: list at least one rule, such as {glob: "prometheus/rules/**", format: prometheus_rules}`,
			"limits.maxFiles: must be from 1 to 25, got 0",
			"limits.maxChangedLines: must be from 1 to 1000, got 0",
			"limits.baseBranch: required",
		}},
		{name: "a token secret without a key", change: func(c *Config) { c.TokenSecret.Key = "" }, want: []string{
			"tokenSecret: required, as { name, key }",
		}},
		{name: "a base URL that is not a URL", change: func(c *Config) { c.BaseURL = "gitlab.example.com" }, want: []string{
			`baseURL: "gitlab.example.com" must be an absolute https URL, such as https://gitlab.example.com`,
		}},
		{name: "plain http on another host", change: func(c *Config) { c.BaseURL = "http://gitlab.example.com" }, want: []string{
			`baseURL: "http://gitlab.example.com" must use https; plain http is only allowed for localhost`,
		}},
		{name: "a base URL that is the API root", change: func(c *Config) { c.BaseURL = "https://gitlab.example.com/api/v4" }, want: []string{
			`baseURL: "https://gitlab.example.com/api/v4" must be the root of the GitLab, without /api/v4`,
		}},
		{name: "projects that are not full paths", change: func(c *Config) {
			c.Limits.Projects = []string{"acme", "acme/*", "../x", "acme/monitors", "acme/**", ""}
		}, want: []string{
			`limits.projects[0]: "acme" must be a full project path such as group/project, with no wildcards`,
			`limits.projects[1]: "acme/*" must be a full project path such as group/project, with no wildcards`,
			`limits.projects[2]: "../x" must be a full project path such as group/project, with no wildcards`,
			`limits.projects[4]: "acme/**" must be a full project path such as group/project, with no wildcards`,
			`limits.projects[5]: "" must be a full project path such as group/project, with no wildcards`,
		}},
		{name: "projects in subgroups", change: func(c *Config) { c.Limits.Projects = []string{"acme/platform/team/monitors", "Acme/Other"} }},
		{name: "path rules that are wrong", change: func(c *Config) {
			c.Limits.Paths = []gitchange.PathRule{{Glob: "", Format: validateclient.FormatTerraform}, {Glob: "a/**"}, {Glob: "b/**", Format: "nagios"}}
		}, want: []string{
			"limits.paths[0].glob: required",
			"limits.paths[1].format: required; use one of prometheus_rules, terraform, grafana_alert_file",
			`limits.paths[2].format: unknown format "nagios"; use one of prometheus_rules, terraform, grafana_alert_file`,
		}},
		{name: "limits out of range", change: func(c *Config) { c.Limits.MaxFiles, c.Limits.MaxChangedLines = 26, 1001 }, want: []string{
			"limits.maxFiles: must be from 1 to 25, got 26",
			"limits.maxChangedLines: must be from 1 to 1000, got 1001",
		}},
		{name: "limits below the range", change: func(c *Config) { c.Limits.MaxFiles, c.Limits.MaxChangedLines = 0, 0 }, want: []string{
			"limits.maxFiles: must be from 1 to 25, got 0",
			"limits.maxChangedLines: must be from 1 to 1000, got 0",
		}},
		{name: "the smallest limits", change: func(c *Config) { c.Limits.MaxFiles, c.Limits.MaxChangedLines = 1, 1 }},
		{name: "the largest limits", change: func(c *Config) { c.Limits.MaxFiles, c.Limits.MaxChangedLines = 25, 1000 }},
		{name: "a base branch with a slash", change: func(c *Config) { c.Limits.BaseBranch = "release/2026.11" }},
		{name: "base branches that are not names", change: func(c *Config) { c.Limits.BaseBranch = "main..x" }, want: []string{
			`limits.baseBranch: "main..x" is not a branch name this action accepts`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{TokenSecret: testToken, Limits: validLimits()}
			if tt.change != nil {
				tt.change(&cfg)
			}

			err := cfg.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, strings.Join(tt.want, "\n"))
		})
	}
}

func TestBaseBranchNames(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		want bool
	}{
		{name: "main", want: true},
		{name: "release/1.2", want: true},
		{name: "feature_x-1", want: true},
		{name: "-main"},
		{name: "main/"},
		{name: "/main"},
		{name: "a//b"},
		{name: "a/.hidden"},
		{name: "with space"},
		{name: "a..b"},
		{name: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := Config{TokenSecret: testToken, Limits: validLimits()}
			cfg.Limits.BaseBranch = tt.name

			require.Equal(t, tt.want, cfg.Validate() == nil)
		})
	}
}

func TestLimitsDraft(t *testing.T) {
	t.Parallel()

	yes, no := true, false

	require.True(t, (&Limits{}).draft(), "the default is a draft")
	require.True(t, (&Limits{Draft: &yes}).draft())
	require.False(t, (&Limits{Draft: &no}).draft())
}

func TestLimitsAllowsProject(t *testing.T) {
	t.Parallel()

	l := Limits{Projects: []string{"acme/platform/monitors"}}

	require.True(t, l.allowsProject("acme/platform/monitors"))
	require.True(t, l.allowsProject("Acme/Platform/Monitors"), "GitLab paths are not case-sensitive")
	require.False(t, l.allowsProject("acme/platform"))
	require.False(t, l.allowsProject("acme/platform/monitors-old"))
	require.False(t, l.allowsProject("acme/platform/monitors/sub"))
	require.False(t, l.allowsProject(""))
}

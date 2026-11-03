package githubpr

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/githubapp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/validateclient"
)

// testApp is a whole GitHub App for tests.
var testApp = githubapp.App{AppID: 11, InstallationID: 22, PrivateKeySecret: registry.SecretRef{Name: "monitor-pr", Key: "key.pem"}}

// validLimits returns limits that pass the config check.
func validLimits() Limits {
	return Limits{
		Repos: []string{"acme/monitors"},
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
		{name: "a base URL for GitHub Enterprise", change: func(c *Config) { c.BaseURL = "https://github.example.com/api/v3" }},
		{name: "plain http on localhost", change: func(c *Config) { c.BaseURL = "http://localhost:8080/" }},
		{name: "nothing set", change: func(c *Config) { *c = Config{} }, want: []string{
			"app.appID: required",
			"app.installationID: required",
			"app.privateKeySecret: required",
			"limits.repos: list at least one repository, as owner/name",
			`limits.paths: list at least one rule, such as {glob: "prometheus/rules/**", format: prometheus_rules}`,
			"limits.maxFiles: must be from 1 to 25, got 0",
			"limits.maxChangedLines: must be from 1 to 1000, got 0",
			"limits.baseBranch: required",
		}},
		{name: "a base URL that is not a URL", change: func(c *Config) { c.BaseURL = "github.example.com" }, want: []string{
			`baseURL: "github.example.com" must be an absolute https URL`,
		}},
		{name: "plain http on another host", change: func(c *Config) { c.BaseURL = "http://github.example.com/api/v3/" }, want: []string{
			`baseURL: "http://github.example.com/api/v3/" must use https; plain http is only allowed for localhost`,
		}},
		{name: "repos that are not names", change: func(c *Config) { c.Limits.Repos = []string{"acme", "acme/*", "../x", "acme/monitors"} }, want: []string{
			`limits.repos[0]: "acme" must have the form owner/name, with no wildcards`,
			`limits.repos[1]: "acme/*" must have the form owner/name, with no wildcards`,
			`limits.repos[2]: "../x" must have the form owner/name, with no wildcards`,
		}},
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

			cfg := Config{App: testApp, Limits: validLimits()}
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

			cfg := Config{App: testApp, Limits: validLimits()}
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

func TestConfigBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{in: "", want: "https://api.github.com/"},
		{in: "https://github.example.com/api/v3", want: "https://github.example.com/api/v3/"},
		{in: "https://github.example.com/api/v3/", want: "https://github.example.com/api/v3/"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, (&Config{BaseURL: tt.in}).baseURL())
		})
	}
}

package gitlabcode

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/config"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(c *Config)
		want   []string
	}{
		{name: "a whole config"},
		{name: "a CI timeout", change: func(c *Config) { c.ReportTimeout = 6 * time.Hour }},
		{name: "a CI timeout over a day", change: func(c *Config) { c.ReportTimeout = 25 * time.Hour }, want: []string{
			"ciReportTimeout: must be from 1m0s to 24h0m0s, got 25h0m0s",
		}},
		{name: "a base URL for a self-managed GitLab", change: func(c *Config) { c.BaseURL = "https://gitlab.example.com" }},
		{name: "plain http on localhost", change: func(c *Config) { c.BaseURL = "http://localhost:8080/" }},
		{name: "refused paths and deletes", change: func(c *Config) {
			c.Limits.RefusedPaths = []string{"internal/legacy/**"}
			c.Limits.AllowDelete = true
		}},
		{name: "nothing set", change: func(c *Config) { *c = Config{} }, want: []string{
			"tokenSecret: required, as { name, key }",
			"limits.projects: list at least one project, as its full path such as acme/platform/monitors",
			`limits.paths: list at least one glob, such as "src/**"`,
			"limits.maxFiles: must be from 1 to 25, got 0",
			"limits.maxChangedLines: must be from 1 to 1000, got 0",
			"limits.baseBranch: required",
		}},
		{name: "a token secret without a key", change: func(c *Config) { c.TokenSecret.Key = "" }, want: []string{
			"tokenSecret: required, as { name, key }",
		}},
		{name: "plain http on another host", change: func(c *Config) { c.BaseURL = "http://gitlab.example.com" }, want: []string{
			`baseURL: "http://gitlab.example.com" must use https; plain http is only allowed for localhost`,
		}},
		{name: "a base URL that is the API root", change: func(c *Config) { c.BaseURL = "https://gitlab.example.com/api/v4" }, want: []string{
			`baseURL: "https://gitlab.example.com/api/v4" must be the root of the GitLab, without /api/v4`,
		}},
		{name: "projects that are not full paths", change: func(c *Config) { c.Limits.Projects = []string{"acme", "acme/*", "acme/app"} }, want: []string{
			`limits.projects[0]: "acme" must be a full project path such as group/project, with no wildcards`,
			`limits.projects[1]: "acme/*" must be a full project path such as group/project, with no wildcards`,
		}},
		{name: "projects in subgroups", change: func(c *Config) { c.Limits.Projects = []string{"acme/platform/team/api", "Acme/Other"} }},
		{name: "globs that are not valid", change: func(c *Config) { c.Limits.Paths = []string{"/src/**", "src/a**b", "src/[a", "src/**"} }, want: []string{
			`limits.paths[0]: "/src/**" must be a path from the root of the repository, without a leading slash`,
			`limits.paths[1]: "src/a**b": ** must be a whole path segment`,
			`limits.paths[2]: "src/[a" is not a valid glob:`,
		}},
		{name: "a refused glob that is not valid", change: func(c *Config) { c.Limits.RefusedPaths = []string{"ok/**", ""} }, want: []string{
			"limits.refusedPaths[1]: required",
		}},
		{name: "maxFiles just inside", change: func(c *Config) { c.Limits.MaxFiles = 25 }},
		{name: "maxFiles just outside", change: func(c *Config) { c.Limits.MaxFiles = 26 }, want: []string{"limits.maxFiles: must be from 1 to 25, got 26"}},
		{name: "maxFiles below", change: func(c *Config) { c.Limits.MaxFiles = 0 }, want: []string{"limits.maxFiles: must be from 1 to 25, got 0"}},
		{name: "maxChangedLines just inside", change: func(c *Config) { c.Limits.MaxChangedLines = 1000 }},
		{name: "maxChangedLines just outside", change: func(c *Config) { c.Limits.MaxChangedLines = 1001 }, want: []string{"limits.maxChangedLines: must be from 1 to 1000, got 1001"}},
		{name: "maxChangedLines below", change: func(c *Config) { c.Limits.MaxChangedLines = 0 }, want: []string{"limits.maxChangedLines: must be from 1 to 1000, got 0"}},
		{name: "a base branch that is not a name", change: func(c *Config) { c.Limits.BaseBranch = "a..b" }, want: []string{
			`limits.baseBranch: "a..b" is not a branch name this action accepts`,
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := Config{TokenSecret: testToken, Limits: validLimits()}
			if tt.change != nil {
				tt.change(&c)
			}

			err := c.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, w := range tt.want {
				require.ErrorContains(t, err, w)
			}
		})
	}
}

const configSection = `
tokenSecret: {name: code-change, key: token}
limits:
  projects: [acme/platform/api]
  paths: ["internal/**", "cmd/**"]
  refusedPaths: ["internal/legacy/**"]
  maxFiles: 2
  maxChangedLines: 30
  baseBranch: main
  allowDelete: true
`

// decodeSection decodes a section the way the config loader does: strictly,
// so a key that is not in the struct is an error.
func decodeSection(t *testing.T, text string) (Config, error) {
	t.Helper()

	var node yaml.Node

	require.NoError(t, yaml.Unmarshal([]byte(text), &node))

	var c Config

	if err := (config.Section{Type: TypeName, Node: node.Content[0]}).Decode(&c); err != nil {
		return c, fmt.Errorf("decode the section: %w", err)
	}

	return c, nil
}

func TestConfigReadsTheSection(t *testing.T) {
	t.Parallel()

	c, err := decodeSection(t, configSection)
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	require.Equal(t, []string{"acme/platform/api"}, c.Limits.Projects)
	require.Equal(t, []string{"internal/**", "cmd/**"}, c.Limits.Paths)
	require.Equal(t, []string{"internal/legacy/**"}, c.Limits.RefusedPaths)
	require.True(t, c.Limits.AllowDelete)
	require.Equal(t, 2, c.Limits.MaxFiles)
}

func TestConfigRejectsADraftKey(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"true", "false"} {
		t.Run("draft: "+value, func(t *testing.T) {
			t.Parallel()

			_, err := decodeSection(t, configSection+"  draft: "+value+"\n")
			require.ErrorContains(t, err, "limits.draft")
			require.ErrorContains(t, err, "unknown key")
		})
	}
}

func TestConfigRejectsOtherUnknownKeys(t *testing.T) {
	t.Parallel()

	_, err := decodeSection(t, configSection+"formats: []\n")
	require.ErrorContains(t, err, "unknown key")

	_, err = decodeSection(t, configSection+"app: {appID: 1}\n")
	require.ErrorContains(t, err, "unknown key")
}

func TestConfigReadsTheCIReportTimeout(t *testing.T) {
	t.Parallel()

	c, err := decodeSection(t, configSection+"ciReportTimeout: 6h\n")
	require.NoError(t, err)
	require.NoError(t, c.Validate())
	require.Equal(t, 6*time.Hour, c.Timeout())

	c, err = decodeSection(t, configSection)
	require.NoError(t, err)
	require.Equal(t, 2*time.Hour, c.Timeout(), "the default is 2 hours")

	c, err = decodeSection(t, configSection+"ciReportTimeout: 48h\n")
	require.NoError(t, err)
	require.ErrorContains(t, c.Validate(), "ciReportTimeout")
}

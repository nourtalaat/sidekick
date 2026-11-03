package launchdarklyflag

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var tokenRef = registry.SecretRef{Name: "launchdarkly-write", Key: "token"}

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		TokenSecret: tokenRef,
		Limits: Limits{Projects: []Project{
			{Key: "shop", Environments: []string{"production"}, Flags: []string{"checkout-*", "new-search"}},
		}},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	longKey := "a" + strings.Repeat("b", 255)

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "the EU base URL", change: func(c *Config) { c.BaseURL = "https://app.eu.launchdarkly.com" }},
		{name: "a base URL on a loopback host", change: func(c *Config) { c.BaseURL = "http://127.0.0.1:8080" }},
		{name: "a flag pattern of only a star", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"*"} }},
		{name: "a key of the longest length", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{longKey} }},
		{name: "two projects", change: func(c *Config) {
			c.Limits.Projects = append(c.Limits.Projects, Project{Key: "search", Environments: []string{"test"}, Flags: []string{"*"}})
		}},
		{name: "a key one longer than the longest", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{longKey + "c"} }, wantErr: "limits.projects[0].flags[0]:"},
		{name: "no token secret", change: func(c *Config) { c.TokenSecret = registry.SecretRef{} }, wantErr: "tokenSecret: required"},
		{name: "a base URL over plain http", change: func(c *Config) { c.BaseURL = "http://ld.example.com" }, wantErr: "baseURL: "},
		{name: "a base URL with a login", change: func(c *Config) { c.BaseURL = "https://user:pass@ld.example.com" }, wantErr: "baseURL: "},
		{name: "a base URL that is not absolute", change: func(c *Config) { c.BaseURL = "app.launchdarkly.com" }, wantErr: "baseURL: "},
		{name: "no projects", change: func(c *Config) { c.Limits.Projects = nil }, wantErr: "limits.projects: list at least one project"},
		{name: "a project key that is not one", change: func(c *Config) { c.Limits.Projects[0].Key = "sh/op" }, wantErr: "limits.projects[0].key:"},
		{name: "an empty project key", change: func(c *Config) { c.Limits.Projects[0].Key = "" }, wantErr: "limits.projects[0].key:"},
		{name: "a project listed twice", change: func(c *Config) { c.Limits.Projects = append(c.Limits.Projects, c.Limits.Projects[0]) }, wantErr: "limits.projects[1].key: \"shop\" is listed twice"},
		{name: "no environments", change: func(c *Config) { c.Limits.Projects[0].Environments = nil }, wantErr: "limits.projects[0].environments: list at least one"},
		{name: "an environment with a star", change: func(c *Config) { c.Limits.Projects[0].Environments = []string{"prod*"} }, wantErr: "limits.projects[0].environments[0]:"},
		{name: "an environment listed twice", change: func(c *Config) { c.Limits.Projects[0].Environments = []string{"production", "production"} }, wantErr: "environments[1]: \"production\" is listed twice"},
		{name: "no flags", change: func(c *Config) { c.Limits.Projects[0].Flags = nil }, wantErr: "limits.projects[0].flags: list at least one"},
		{name: "a flag with a slash", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"a/b"} }, wantErr: "limits.projects[0].flags[0]:"},
		{name: "a flag pattern with a question mark", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"a?"} }, wantErr: "limits.projects[0].flags[0]:"},
		{name: "a flag pattern with brackets", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"[a]*"} }, wantErr: "limits.projects[0].flags[0]:"},
		{name: "a flag listed twice", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"a", "a"} }, wantErr: "flags[1]: \"a\" is listed twice"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			c := validConfig()
			tt.change(&c)

			err := c.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConfigDecodesTheGate(t *testing.T) {
	t.Parallel()

	const doc = `
allowReadWrite: true
tokenSecret: { name: launchdarkly-write, key: token }
limits:
  projects:
    - key: shop
      environments: [production]
      flags: [checkout-*]
`

	var cfg Config

	require.NoError(t, yaml.Unmarshal([]byte(doc), &cfg))
	require.NoError(t, cfg.Validate())
	require.True(t, cfg.AllowReadWrite)
	require.False(t, action.ReadWriteBlocked(&cfg))
	require.True(t, action.ReadWriteBlocked(&Config{}))
}

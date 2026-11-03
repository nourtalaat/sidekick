package unleashflag

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var tokenRef = registry.SecretRef{Name: "unleash-write", Key: "token"}

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		BaseURL:     "https://unleash.example.com",
		TokenSecret: tokenRef,
		Limits: Limits{Projects: []Project{
			{Key: "default", Environments: []string{"production"}, Flags: []string{"checkout-*", "new-search"}},
		}},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	longKey := strings.Repeat("a", 256)

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "a base url with a path", change: func(c *Config) { c.BaseURL = "https://example.com/unleash" }},
		{name: "a base url on a loopback host", change: func(c *Config) { c.BaseURL = "http://127.0.0.1:4242" }},
		{name: "turn_off listed", change: func(c *Config) { c.Limits.Allow = []string{"turn_off"} }},
		{name: "restore_previous listed", change: func(c *Config) { c.Limits.Allow = []string{"restore_previous"} }},
		{name: "both changes listed", change: func(c *Config) { c.Limits.Allow = []string{"turn_off", "restore_previous"} }},
		{name: "a key of the longest length", change: func(c *Config) { c.Limits.Projects[0].Key = longKey }},
		{name: "a flag pattern of only a star", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"*"} }},
		{name: "two projects", change: func(c *Config) {
			c.Limits.Projects = append(c.Limits.Projects, Project{Key: "payments", Environments: []string{"staging"}, Flags: []string{"*"}})
		}},
		{name: "no base url", change: func(c *Config) { c.BaseURL = "" }, wantErr: "baseURL: required"},
		{name: "a base url over plain http", change: func(c *Config) { c.BaseURL = "http://unleash.example.com" }, wantErr: "baseURL: "},
		{name: "a base url with a login", change: func(c *Config) { c.BaseURL = "https://user:pass@unleash.example.com" }, wantErr: "baseURL: "},
		{name: "a base url with a query", change: func(c *Config) { c.BaseURL = "https://unleash.example.com?a=b" }, wantErr: "baseURL: "},
		{name: "a base url that is not absolute", change: func(c *Config) { c.BaseURL = "unleash.example.com" }, wantErr: "baseURL: "},
		{name: "no token secret", change: func(c *Config) { c.TokenSecret = registry.SecretRef{} }, wantErr: "tokenSecret: required"},
		{name: "no projects", change: func(c *Config) { c.Limits.Projects = nil }, wantErr: "limits.projects: list at least one project"},
		{name: "a project key with a slash", change: func(c *Config) { c.Limits.Projects[0].Key = "a/b" }, wantErr: "limits.projects[0].key"},
		{name: "a project key one over the length", change: func(c *Config) { c.Limits.Projects[0].Key = longKey + "a" }, wantErr: "limits.projects[0].key"},
		{name: "a project listed twice", change: func(c *Config) {
			c.Limits.Projects = append(c.Limits.Projects, c.Limits.Projects[0])
		}, wantErr: `limits.projects[1].key: "default" is listed twice`},
		{name: "no environments", change: func(c *Config) { c.Limits.Projects[0].Environments = nil }, wantErr: "limits.projects[0].environments: list at least one"},
		{name: "an environment with a colon", change: func(c *Config) { c.Limits.Projects[0].Environments = []string{"a:b"} }, wantErr: "limits.projects[0].environments[0]"},
		{name: "an environment listed twice", change: func(c *Config) {
			c.Limits.Projects[0].Environments = []string{"production", "production"}
		}, wantErr: `limits.projects[0].environments[1]: "production" is listed twice`},
		{name: "no flags", change: func(c *Config) { c.Limits.Projects[0].Flags = nil }, wantErr: "limits.projects[0].flags: list at least one"},
		{name: "a flag pattern with a question mark", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"a?"} }, wantErr: "limits.projects[0].flags[0]"},
		{name: "a flag pattern with a bracket", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"a[b]"} }, wantErr: "limits.projects[0].flags[0]"},
		{name: "a flag pattern with a slash", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"a/b"} }, wantErr: "limits.projects[0].flags[0]"},
		{name: "a flag listed twice", change: func(c *Config) { c.Limits.Projects[0].Flags = []string{"a", "a"} }, wantErr: `limits.projects[0].flags[1]: "a" is listed twice`},
		{name: "a change that does not exist", change: func(c *Config) { c.Limits.Allow = []string{"turn_on"} }, wantErr: `limits.allow[0]: "turn_on" is not a change; use turn_off or restore_previous`},
		{name: "a change listed twice", change: func(c *Config) { c.Limits.Allow = []string{"turn_off", "turn_off"} }, wantErr: `limits.allow[1]: "turn_off" is listed twice`},
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

func TestConfigReadsYAML(t *testing.T) {
	t.Parallel()

	const section = `
baseURL: https://unleash.example.com
tokenSecret: { name: unleash-write, key: token }
allowReadWrite: true
limits:
  projects:
    - key: default
      environments: [production, staging]
      flags: ["checkout-*"]
  allow: [turn_off, restore_previous]
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		ReadWriteGate: action.ReadWriteGate{AllowReadWrite: true},
		BaseURL:       "https://unleash.example.com",
		TokenSecret:   tokenRef,
		Limits: Limits{
			Projects: []Project{{Key: "default", Environments: []string{"production", "staging"}, Flags: []string{"checkout-*"}}},
			Allow:    []string{"turn_off", "restore_previous"},
		},
	}, c)
}

func TestConfigIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "without the opt-in", yaml: "baseURL: https://u.example.com", want: true},
		{name: "with the opt-in", yaml: "allowReadWrite: true", want: false},
		{name: "with the opt-in turned off", yaml: "allowReadWrite: false", want: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var c Config

			require.NoError(t, yaml.Unmarshal([]byte(tt.yaml), &c))
			require.Equal(t, tt.want, action.ReadWriteBlocked(&c))
		})
	}
}

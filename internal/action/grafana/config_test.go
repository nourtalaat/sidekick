package grafana

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/registry"
)

var tokenRef = registry.SecretRef{Name: "grafana-write", Key: "token"}

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		URL:         "https://acme.grafana.net",
		TokenSecret: tokenRef,
		Limits:      Limits{FolderUIDs: []string{"payments"}, Fields: []string{"for", "data"}},
	}
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "a url on a loopback host", change: func(c *Config) { c.URL = "http://127.0.0.1:3000" }},
		{name: "a url with a sub-path", change: func(c *Config) { c.URL = "https://example.com/grafana/" }},
		{name: "delete without fields", change: func(c *Config) { c.Limits.Fields, c.Limits.AllowDelete = nil, true }},
		{name: "narrowed by UID and pattern", change: func(c *Config) {
			c.Limits.RuleUIDs, c.Limits.TitlePatterns = []string{"abc_1"}, []string{"Checkout *"}
		}},
		{name: "a nested field with a position", change: func(c *Config) { c.Limits.Fields = []string{"data[0].model.conditions"} }},
		{name: "no url", change: func(c *Config) { c.URL = "" }, wantErr: "url: required"},
		{name: "a url over plain http", change: func(c *Config) { c.URL = "http://grafana.example.com" }, wantErr: "url: "},
		{name: "a url with a login", change: func(c *Config) { c.URL = "https://user:pass@grafana.example.com" }, wantErr: "url: "},
		{name: "a url with a query", change: func(c *Config) { c.URL = "https://grafana.example.com/?x=1" }, wantErr: "url: "},
		{name: "a url that is not absolute", change: func(c *Config) { c.URL = "grafana.example.com" }, wantErr: "url: "},
		{name: "no token", change: func(c *Config) { c.TokenSecret = registry.SecretRef{} }, wantErr: "tokenSecret: required"},
		{name: "no folders", change: func(c *Config) { c.Limits.FolderUIDs = nil }, wantErr: "limits.folderUids: at least one"},
		{name: "a folder UID with a slash", change: func(c *Config) { c.Limits.FolderUIDs = []string{"a/b"} }, wantErr: `limits.folderUids: "a/b"`},
		{name: "a folder UID that is too long", change: func(c *Config) { c.Limits.FolderUIDs = []string{strings.Repeat("a", 41)} }, wantErr: "limits.folderUids"},
		{name: "a rule UID that is not one", change: func(c *Config) { c.Limits.RuleUIDs = []string{"a b"} }, wantErr: "limits.ruleUids"},
		{name: "an empty pattern", change: func(c *Config) { c.Limits.TitlePatterns = []string{" "} }, wantErr: "limits.titlePatterns: a pattern must not be empty"},
		{name: "no fields and no delete", change: func(c *Config) { c.Limits.Fields = nil }, wantErr: "limits.fields: at least one field"},
		{name: "a field that is not a path", change: func(c *Config) { c.Limits.Fields = []string{"data..x"} }, wantErr: "limits.fields:"},
		{name: "the folder cannot change", change: func(c *Config) { c.Limits.Fields = []string{"folderUID"} }, wantErr: `limits.fields: "folderUID": the first key must be one of`},
		{name: "the group cannot change", change: func(c *Config) { c.Limits.Fields = []string{"ruleGroup"} }, wantErr: "limits.fields:"},
		{name: "notification settings are not editable", change: func(c *Config) { c.Limits.Fields = []string{"notification_settings"} }, wantErr: "limits.fields:"},
		{name: "a read-only field", change: func(c *Config) { c.Limits.Fields = []string{"provenance"} }, wantErr: "limits.fields:"},
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
url: https://acme.grafana.net/
tokenSecret: { name: grafana-write, key: token }
limits:
  folderUids: [payments, search]
  ruleUids: [abc123]
  titlePatterns: ["Checkout *"]
  fields: [for, data]
  allowDelete: true
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		URL: "https://acme.grafana.net/", TokenSecret: tokenRef,
		Limits: Limits{
			FolderUIDs: []string{"payments", "search"}, RuleUIDs: []string{"abc123"},
			TitlePatterns: []string{"Checkout *"}, Fields: []string{"for", "data"}, AllowDelete: true,
		},
	}, c)
	require.Equal(t, "https://acme.grafana.net", c.apiURL(), "no trailing slash")
}

func TestRuleAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		limits   Limits
		uid      string
		title    string
		wantPass bool
	}{
		{name: "no narrowing allows every rule", limits: Limits{}, uid: "x", title: "Anything", wantPass: true},
		{name: "a listed UID", limits: Limits{RuleUIDs: []string{"a1"}}, uid: "a1", title: "Other", wantPass: true},
		{name: "an unlisted UID", limits: Limits{RuleUIDs: []string{"a1"}}, uid: "a2", title: "Other"},
		{name: "a pattern with a wildcard at the end", limits: Limits{TitlePatterns: []string{"Checkout *"}}, uid: "x", title: "Checkout latency", wantPass: true},
		{name: "a pattern matches the whole title", limits: Limits{TitlePatterns: []string{"Checkout *"}}, uid: "x", title: "Old Checkout latency"},
		{name: "a wildcard in the middle", limits: Limits{TitlePatterns: []string{"A*Z"}}, uid: "x", title: "A and then Z", wantPass: true},
		{name: "case counts", limits: Limits{TitlePatterns: []string{"checkout *"}}, uid: "x", title: "Checkout latency"},
		{name: "other characters stand for themselves", limits: Limits{TitlePatterns: []string{"a.c (x)"}}, uid: "x", title: "abc (x)"},
		{name: "a literal dot matches", limits: Limits{TitlePatterns: []string{"a.c (x)"}}, uid: "x", title: "a.c (x)", wantPass: true},
		{name: "a wildcard spans lines", limits: Limits{TitlePatterns: []string{"a*b"}}, uid: "x", title: "a\nb", wantPass: true},
		{name: "a UID or a pattern", limits: Limits{RuleUIDs: []string{"a1"}, TitlePatterns: []string{"Checkout *"}}, uid: "a1", title: "Other", wantPass: true},
		{name: "neither the UID nor the pattern", limits: Limits{RuleUIDs: []string{"a1"}, TitlePatterns: []string{"Checkout *"}}, uid: "a2", title: "Other"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			patterns, err := compilePatterns(tt.limits.TitlePatterns)
			require.NoError(t, err)
			require.Equal(t, tt.wantPass, tt.limits.ruleAllowed(tt.uid, tt.title, patterns))
		})
	}
}

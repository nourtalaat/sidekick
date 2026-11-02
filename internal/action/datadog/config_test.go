package datadog

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/registry"
)

var (
	apiRef = registry.SecretRef{Name: "datadog-write", Key: "apiKey"}
	appRef = registry.SecretRef{Name: "datadog-write", Key: "appKey"}
)

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		APIKeySecret: apiRef, AppKeySecret: appRef,
		Limits: Limits{MonitorIDs: []int64{123}, Fields: []string{"options.thresholds", "message"}},
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
		{name: "a regional site", change: func(c *Config) { c.Site = "us3.datadoghq.com" }},
		{name: "a url on a loopback host", change: func(c *Config) { c.URL = "http://127.0.0.1:8080" }},
		{name: "an https url", change: func(c *Config) { c.URL = "https://datadog-proxy.example.com" }},
		{name: "tags instead of IDs", change: func(c *Config) { c.Limits.MonitorIDs, c.Limits.MonitorTags = nil, []string{"team:payments"} }},
		{name: "delete without fields", change: func(c *Config) { c.Limits.Fields, c.Limits.AllowDelete = nil, true }},
		{name: "a nested field with a position", change: func(c *Config) { c.Limits.Fields = []string{"options.variables[*].query"} }},
		{name: "site and url", change: func(c *Config) { c.Site, c.URL = "datadoghq.eu", "https://x.example.com" }, wantErr: "url: cannot be set together with site"},
		{name: "a site that is not a host", change: func(c *Config) { c.Site = "datadoghq.com/evil" }, wantErr: "site:"},
		{name: "a site with no dot", change: func(c *Config) { c.Site = "localhost" }, wantErr: "site:"},
		{name: "a url over plain http", change: func(c *Config) { c.URL = "http://datadog.example.com" }, wantErr: "url: "},
		{name: "a url with a login", change: func(c *Config) { c.URL = "https://user:pass@datadog.example.com" }, wantErr: "url: "},
		{name: "no api key", change: func(c *Config) { c.APIKeySecret = registry.SecretRef{} }, wantErr: "apiKeySecret: required"},
		{name: "no app key", change: func(c *Config) { c.AppKeySecret = registry.SecretRef{} }, wantErr: "appKeySecret: required"},
		{name: "no monitors", change: func(c *Config) { c.Limits.MonitorIDs = nil }, wantErr: "limits.monitorIds"},
		{name: "a monitor ID that is not one", change: func(c *Config) { c.Limits.MonitorIDs = []int64{0} }, wantErr: "limits.monitorIds: 0 is not a monitor ID"},
		{name: "an empty tag", change: func(c *Config) { c.Limits.MonitorTags = []string{" "} }, wantErr: "limits.monitorTags"},
		{name: "no fields and no delete", change: func(c *Config) { c.Limits.Fields = nil }, wantErr: "limits.fields: at least one field"},
		{name: "a field that is not a path", change: func(c *Config) { c.Limits.Fields = []string{"options..x"} }, wantErr: "limits.fields:"},
		{name: "a read-only field", change: func(c *Config) { c.Limits.Fields = []string{"creator"} }, wantErr: `limits.fields: "creator": the first key must be one of`},
		{name: "the type cannot change", change: func(c *Config) { c.Limits.Fields = []string{"type"} }, wantErr: "limits.fields:"},
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
site: datadoghq.eu
apiKeySecret: { name: datadog-write, key: apiKey }
appKeySecret: { name: datadog-write, key: appKey }
limits:
  monitorIds: [123, 456]
  monitorTags: ["team:payments"]
  fields: [options.thresholds, message]
  allowDelete: true
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		Site: "datadoghq.eu", APIKeySecret: apiRef, AppKeySecret: appRef,
		Limits: Limits{MonitorIDs: []int64{123, 456}, MonitorTags: []string{"team:payments"}, Fields: []string{"options.thresholds", "message"}, AllowDelete: true},
	}, c)
	require.Equal(t, "https://api.datadoghq.eu", c.apiURL())
}

func TestAddresses(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://api.datadoghq.com", (&Config{}).apiURL(), "the default site")
	require.Equal(t, "https://api.us3.datadoghq.com", (&Config{Site: "us3.datadoghq.com"}).apiURL())
	require.Equal(t, "http://127.0.0.1:9", (&Config{URL: "http://127.0.0.1:9/"}).apiURL(), "a url replaces the site")

	require.Equal(t, "app.datadoghq.com", appHost("datadoghq.com"))
	require.Equal(t, "app.datadoghq.eu", appHost("datadoghq.eu"))
	require.Equal(t, "us3.datadoghq.com", appHost("us3.datadoghq.com"))
}

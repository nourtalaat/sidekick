package datadogdowntime

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action/mute"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	with := func(change func(*Config)) Config {
		c := testConfig("https://api.datadoghq.com")
		change(&c)

		return c
	}

	tests := []struct {
		name    string
		cfg     Config
		wantErr []string
	}{
		{name: "the smallest config", cfg: testConfig("")},
		{name: "a site", cfg: with(func(c *Config) { c.URL = ""; c.Site = "datadoghq.eu" })},
		{name: "http to a loopback host", cfg: with(func(c *Config) { c.URL = "http://127.0.0.1:9" })},
		{name: "only monitor tags", cfg: with(func(c *Config) { c.Limits.MonitorIDs = nil })},
		{name: "only monitor IDs", cfg: with(func(c *Config) { c.Limits.MonitorTags = nil })},
		{name: "http to another host", cfg: with(func(c *Config) { c.URL = "http://api.example.com" }), wantErr: []string{"must use https"}},
		{name: "a site and a url", cfg: with(func(c *Config) { c.Site = "datadoghq.eu" }), wantErr: []string{"url: cannot be set together with site"}},
		{name: "no api key", cfg: with(func(c *Config) { c.APIKeySecret = registry.SecretRef{} }), wantErr: []string{"apiKeySecret: required"}},
		{name: "no app key", cfg: with(func(c *Config) { c.AppKeySecret = registry.SecretRef{} }), wantErr: []string{"appKeySecret: required"}},
		{
			name: "no monitors", cfg: with(func(c *Config) { c.Limits.MonitorIDs, c.Limits.MonitorTags = nil, nil }),
			wantErr: []string{"limits.monitorIds: at least one monitor ID, or limits.monitorTags"},
		},
		{name: "a monitor ID of zero", cfg: with(func(c *Config) { c.Limits.MonitorIDs = []int64{0} }), wantErr: []string{"limits.monitorIds: 0 is not a monitor ID"}},
		{name: "an empty tag", cfg: with(func(c *Config) { c.Limits.MonitorTags = []string{" "} }), wantErr: []string{"limits.monitorTags[0]: must not be empty"}},
		{
			name: "the tag that mutes every monitor", cfg: with(func(c *Config) { c.Limits.MonitorTags = []string{"*"} }),
			wantErr: []string{"limits.monitorTags[0]", "would mute every monitor"},
		},
		{name: "a tag listed twice", cfg: with(func(c *Config) { c.Limits.MonitorTags = []string{"a:b", "a:b"} }), wantErr: []string{"limits.monitorTags[1]", "listed twice"}},
		{name: "no scopes", cfg: with(func(c *Config) { c.Limits.Scopes = nil }), wantErr: []string{"limits.scopes: at least one scope is required"}},
		{name: "the scope * when it is listed", cfg: with(func(c *Config) { c.Limits.Scopes = []string{"*"} })},
		{name: "an empty scope", cfg: with(func(c *Config) { c.Limits.Scopes = []string{""} }), wantErr: []string{"limits.scopes[0]: must not be empty"}},
		{name: "a scope with a space at its end", cfg: with(func(c *Config) { c.Limits.Scopes = []string{"env:prod "} }), wantErr: []string{"limits.scopes[0]", "space"}},
		{name: "a scope with a line break", cfg: with(func(c *Config) { c.Limits.Scopes = []string{"env:prod\nhost:a"} }), wantErr: []string{"limits.scopes[0]: must not hold control characters"}},
		{name: "a scope listed twice", cfg: with(func(c *Config) { c.Limits.Scopes = []string{"a:b", "a:b"} }), wantErr: []string{"limits.scopes[1]", "listed twice"}},
		{name: "the longest scope", cfg: with(func(c *Config) { c.Limits.Scopes = []string{strings.Repeat("a", maxScopeBytes)} })},
		{
			name: "a scope too long", cfg: with(func(c *Config) { c.Limits.Scopes = []string{strings.Repeat("a", maxScopeBytes+1)} }),
			wantErr: []string{"limits.scopes[0]: longer than 500 bytes"},
		},
		{name: "the shortest maxDuration", cfg: with(func(c *Config) { c.Limits.MaxDuration = mute.MinDuration })},
		{name: "just below the shortest maxDuration", cfg: with(func(c *Config) { c.Limits.MaxDuration = mute.MinDuration - time.Second }), wantErr: []string{"limits.maxDuration: "}},
		{name: "the ceiling", cfg: with(func(c *Config) { c.Limits.MaxDuration = mute.CeilingMaxDuration })},
		{
			name:    "just above the ceiling",
			cfg:     with(func(c *Config) { c.Limits.MaxDuration = mute.CeilingMaxDuration + time.Second }),
			wantErr: []string{"limits.maxDuration: ", "above 24h"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()

			if len(tt.wantErr) == 0 {
				require.NoError(t, err)

				return
			}

			require.Error(t, err)

			for _, text := range tt.wantErr {
				require.ErrorContains(t, err, text)
			}
		})
	}
}

func TestConfigFromYAML(t *testing.T) {
	t.Parallel()

	section := `
site: datadoghq.eu
apiKeySecret: { name: sidekick-datadog-downtime, key: apiKey }
appKeySecret: { name: sidekick-datadog-downtime, key: appKey }
limits:
  monitorIds: [123, 456]
  monitorTags: ["team:payments"]
  scopes: ["env:prod", "env:staging AND service:checkout"]
  maxDuration: 4h
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		Site:         "datadoghq.eu",
		APIKeySecret: registry.SecretRef{Name: "sidekick-datadog-downtime", Key: "apiKey"},
		AppKeySecret: registry.SecretRef{Name: "sidekick-datadog-downtime", Key: "appKey"},
		Limits: Limits{
			MonitorIDs: []int64{123, 456}, MonitorTags: []string{"team:payments"},
			Scopes: []string{"env:prod", "env:staging AND service:checkout"}, MaxDuration: 4 * time.Hour,
		},
	}, c)
	require.Equal(t, "https://api.datadoghq.eu", c.apiURL())
}

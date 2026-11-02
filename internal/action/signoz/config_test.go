package signoz

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/registry"
)

var keyRef = registry.SecretRef{Name: "signoz-write", Key: "apiKey"}

// ruleOne and ruleTwo are rule IDs for the tests.
const (
	ruleOne = "0196a1b2-7c3d-7e4f-8a5b-1c2d3e4f5a6b"
	ruleTwo = "0196a1b2-7c3d-7e4f-8a5b-1c2d3e4f5a6c"
)

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		URL: "https://example.signoz.cloud", APIKeySecret: keyRef,
		Limits: Limits{RuleIDs: []string{ruleOne}, Fields: []string{"condition.thresholds", "labels"}},
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
		{name: "a url on a loopback host", change: func(c *Config) { c.URL = "http://127.0.0.1:8080" }},
		{name: "a url with a path", change: func(c *Config) { c.URL = "https://example.com/signoz/" }},
		{name: "a header of its own", change: func(c *Config) { c.APIKeyHeader = "X-Key" }},
		{name: "names instead of IDs", change: func(c *Config) { c.Limits.RuleIDs, c.Limits.RuleNames = nil, []string{"checkout *"} }},
		{name: "an ID in capitals", change: func(c *Config) { c.Limits.RuleIDs = []string{"0196A1B2-7C3D-7E4F-8A5B-1C2D3E4F5A6B"} }},
		{name: "delete without fields", change: func(c *Config) { c.Limits.Fields, c.Limits.AllowDelete = nil, true }},
		{name: "a nested field with a position", change: func(c *Config) { c.Limits.Fields = []string{"condition.thresholds.spec[*].target"} }},
		{name: "one label", change: func(c *Config) { c.Limits.Fields = []string{"labels.team"} }},
		{name: "no url", change: func(c *Config) { c.URL = "" }, wantErr: "url: required"},
		{name: "a url over plain http", change: func(c *Config) { c.URL = "http://signoz.example.com" }, wantErr: "url: "},
		{name: "a url with a login", change: func(c *Config) { c.URL = "https://user:pass@signoz.example.com" }, wantErr: "url: "},
		{name: "a url with a query", change: func(c *Config) { c.URL = "https://signoz.example.com?x=1" }, wantErr: "url: "},
		{name: "a url that is no address", change: func(c *Config) { c.URL = "signoz" }, wantErr: "url: "},
		{name: "no key", change: func(c *Config) { c.APIKeySecret = registry.SecretRef{} }, wantErr: "apiKeySecret: required"},
		{name: "a header that is not a name", change: func(c *Config) { c.APIKeyHeader = "Bad Header" }, wantErr: "apiKeyHeader:"},
		{name: "no rules", change: func(c *Config) { c.Limits.RuleIDs = nil }, wantErr: "limits.ruleIds"},
		{name: "an ID that is not a UUID", change: func(c *Config) { c.Limits.RuleIDs = []string{"12345"} }, wantErr: `limits.ruleIds: "12345" is not a rule ID`},
		{name: "an empty pattern", change: func(c *Config) { c.Limits.RuleNames = []string{" "} }, wantErr: "limits.ruleNames"},
		{name: "no fields and no delete", change: func(c *Config) { c.Limits.Fields = nil }, wantErr: "limits.fields: at least one field"},
		{name: "a field that is not a path", change: func(c *Config) { c.Limits.Fields = []string{"labels..x"} }, wantErr: "limits.fields:"},
		{name: "a field that cannot be changed", change: func(c *Config) { c.Limits.Fields = []string{"alertType"} }, wantErr: `limits.fields: "alertType" is not a field Sidekick can change`},
		{name: "a read-only field", change: func(c *Config) { c.Limits.Fields = []string{"updatedAt"} }, wantErr: "limits.fields:"},
		{name: "a typo", change: func(c *Config) { c.Limits.Fields = []string{"condition.targt"} }, wantErr: "limits.fields:"},
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
url: https://example.signoz.cloud
apiKeySecret: { name: signoz-write, key: apiKey }
apiKeyHeader: X-Key
limits:
  ruleIds: ["0196a1b2-7c3d-7e4f-8a5b-1c2d3e4f5a6b"]
  ruleNames: ["checkout *"]
  fields: [condition.thresholds, labels]
  allowDelete: true
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		URL: "https://example.signoz.cloud", APIKeySecret: keyRef, APIKeyHeader: "X-Key",
		Limits: Limits{RuleIDs: []string{ruleOne}, RuleNames: []string{"checkout *"}, Fields: []string{"condition.thresholds", "labels"}, AllowDelete: true},
	}, c)
}

func TestAddressAndHeader(t *testing.T) {
	t.Parallel()

	require.Equal(t, "https://example.com/signoz", (&Config{URL: "https://example.com/signoz/"}).apiURL())
	require.Equal(t, "http://127.0.0.1:9", (&Config{URL: "http://127.0.0.1:9"}).apiURL())
	require.Equal(t, "SIGNOZ-API-KEY", (&Config{}).keyHeader(), "the default header")
	require.Equal(t, "X-Key", (&Config{APIKeyHeader: "X-Key"}).keyHeader())
}

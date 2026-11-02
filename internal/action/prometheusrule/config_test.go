package prometheusrule

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		Limits: Limits{
			Namespaces: []string{"monitoring"},
			Names:      []string{"checkout-alerts"},
			Groups:     []string{"checkout.rules"},
			Fields:     []string{"expr", "for", "labels.severity"},
		},
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
		{name: "a service account", change: func(c *Config) { c.ServiceAccount = "team-actions" }},
		{name: "every editable field", change: func(c *Config) {
			c.Limits.Fields = []string{"expr", "for", "keep_firing_for", "labels", "annotations.summary"}
		}},
		{name: "a group name with dots and spaces", change: func(c *Config) { c.Limits.Groups = []string{"kubernetes.rules and more"} }},
		{name: "a bad service account", change: func(c *Config) { c.ServiceAccount = "Not_Valid" }, wantErr: "serviceAccount:"},
		{name: "no namespaces", change: func(c *Config) { c.Limits.Namespaces = nil }, wantErr: "limits.namespaces: at least one entry"},
		{name: "a bad namespace", change: func(c *Config) { c.Limits.Namespaces = []string{"Monitoring"} }, wantErr: "limits.namespaces:"},
		{name: "a namespace twice", change: func(c *Config) { c.Limits.Namespaces = []string{"a", "a"} }, wantErr: `limits.namespaces: "a" is listed twice`},
		{name: "no names", change: func(c *Config) { c.Limits.Names = nil }, wantErr: "limits.names: at least one entry"},
		{name: "a glob as a name", change: func(c *Config) { c.Limits.Names = []string{"checkout-*"} }, wantErr: "limits.names:"},
		{name: "no groups", change: func(c *Config) { c.Limits.Groups = nil }, wantErr: "limits.groups: at least one entry"},
		{name: "an empty group", change: func(c *Config) { c.Limits.Groups = []string{" "} }, wantErr: "limits.groups:"},
		{name: "a group that starts with a bracket", change: func(c *Config) { c.Limits.Groups = []string{"[x]"} }, wantErr: "limits.groups:"},
		{name: "a group twice", change: func(c *Config) { c.Limits.Groups = []string{"g", "g"} }, wantErr: `limits.groups: "g" is listed twice`},
		{name: "no fields", change: func(c *Config) { c.Limits.Fields = nil }, wantErr: "limits.fields: at least one field"},
		{name: "a field that is not a path", change: func(c *Config) { c.Limits.Fields = []string{"labels..x"} }, wantErr: "limits.fields:"},
		{name: "the alert name cannot change", change: func(c *Config) { c.Limits.Fields = []string{"alert"} }, wantErr: `limits.fields: "alert": the first key must be one of`},
		{name: "the whole rule cannot change", change: func(c *Config) { c.Limits.Fields = []string{"record"} }, wantErr: "the first key must be one of"},
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
serviceAccount: sidekick-actions
limits:
  namespaces: [monitoring, staging]
  names: [checkout-alerts]
  groups: [checkout.rules]
  fields: [expr, for, labels]
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		ServiceAccount: "sidekick-actions",
		Limits: Limits{
			Namespaces: []string{"monitoring", "staging"}, Names: []string{"checkout-alerts"},
			Groups: []string{"checkout.rules"}, Fields: []string{"expr", "for", "labels"},
		},
	}, c)
}

func TestServiceAccountDefault(t *testing.T) {
	t.Parallel()

	require.Equal(t, DefaultServiceAccount, (&Config{}).serviceAccount())
	require.Equal(t, "other", (&Config{ServiceAccount: "other"}).serviceAccount())
}

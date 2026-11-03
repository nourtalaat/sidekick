package kubernetesrollback

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
)

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{Limits: Limits{
		Namespaces: []string{testNamespace},
		Names:      []string{deploymentName, statefulSetName, daemonSetName},
	}}
}

// ptr returns a pointer to n.
func ptr(n int) *int { return &n }

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "every kind listed", change: func(c *Config) { c.Limits.Kinds = allKinds }},
		{name: "one kind listed", change: func(c *Config) { c.Limits.Kinds = []string{"DaemonSet"} }},
		{name: "a service account of the longest valid name", change: func(c *Config) { c.ServiceAccount = strings.Repeat("a", 253) }},
		{name: "a namespace of the longest length", change: func(c *Config) { c.Limits.Namespaces = []string{strings.Repeat("a", 63)} }},
		{name: "maxRevisionsBack at the lowest", change: func(c *Config) { c.MaxRevisionsBack = ptr(1) }},
		{name: "maxRevisionsBack of two", change: func(c *Config) { c.MaxRevisionsBack = ptr(2) }},
		{name: "maxRevisionsBack at the highest", change: func(c *Config) { c.MaxRevisionsBack = ptr(action.MaxRevisionsBackCeiling) }},
		{name: "maxRevisionsBack of zero", change: func(c *Config) { c.MaxRevisionsBack = ptr(0) }, wantErr: "maxRevisionsBack: 0 must be from 1 to 10"},
		{name: "maxRevisionsBack one over the highest", change: func(c *Config) { c.MaxRevisionsBack = ptr(action.MaxRevisionsBackCeiling + 1) }, wantErr: "maxRevisionsBack: 11 must be from 1 to 10"},
		{name: "a service account name that is not valid", change: func(c *Config) { c.ServiceAccount = "Not Valid" }, wantErr: "serviceAccount:"},
		{name: "a service account name one over the length", change: func(c *Config) { c.ServiceAccount = strings.Repeat("a", 254) }, wantErr: "serviceAccount:"},
		{name: "no namespaces", change: func(c *Config) { c.Limits.Namespaces = nil }, wantErr: "limits.namespaces: at least one entry is required"},
		{name: "a namespace one over the length", change: func(c *Config) { c.Limits.Namespaces = []string{strings.Repeat("a", 64)} }, wantErr: "limits.namespaces:"},
		{name: "a namespace that is not valid", change: func(c *Config) { c.Limits.Namespaces = []string{"Shop"} }, wantErr: "limits.namespaces:"},
		{name: "a namespace listed twice", change: func(c *Config) { c.Limits.Namespaces = []string{"shop", "shop"} }, wantErr: `limits.namespaces: "shop" is listed twice`},
		{name: "no names", change: func(c *Config) { c.Limits.Names = nil }, wantErr: "limits.names: at least one entry is required"},
		{name: "a name with a star", change: func(c *Config) { c.Limits.Names = []string{"check*"} }, wantErr: "limits.names:"},
		{name: "a name listed twice", change: func(c *Config) { c.Limits.Names = []string{"a", "a"} }, wantErr: `limits.names: "a" is listed twice`},
		{name: "a kind that is not supported", change: func(c *Config) { c.Limits.Kinds = []string{"Job"} }, wantErr: `limits.kinds: "Job" is not valid`},
		{name: "a kind in the wrong case", change: func(c *Config) { c.Limits.Kinds = []string{"deployment"} }, wantErr: `limits.kinds: "deployment" is not valid`},
		{name: "a kind listed twice", change: func(c *Config) { c.Limits.Kinds = []string{"Deployment", "Deployment"} }, wantErr: `limits.kinds: "Deployment" is listed twice`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.change(&cfg)

			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestConfigDecodes(t *testing.T) {
	t.Parallel()

	const text = `
serviceAccount: rollback-account
allowReadWrite: true
maxRevisionsBack: 3
limits:
  namespaces: [shop]
  kinds: [Deployment]
  names: [checkout]
`

	var cfg Config

	dec := yaml.NewDecoder(strings.NewReader(text))
	dec.KnownFields(true)
	require.NoError(t, dec.Decode(&cfg))
	require.NoError(t, cfg.Validate())
	require.True(t, cfg.AllowReadWrite)
	require.Equal(t, 3, cfg.Max())
	require.Equal(t, "rollback-account", cfg.serviceAccount())
	require.Equal(t, []string{"Deployment"}, cfg.Limits.kinds())

	var unknown Config

	dec = yaml.NewDecoder(strings.NewReader("limits: {namespaces: [shop], names: [a], revisions: 2}"))
	dec.KnownFields(true)
	require.Error(t, dec.Decode(&unknown), "a key that is not in the config is refused")
}

func TestConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg := validConfig()

	require.Equal(t, "sidekick-kubernetes-rollback", cfg.serviceAccount())
	require.Equal(t, []string{"Deployment", "StatefulSet", "DaemonSet"}, cfg.Limits.kinds())
	require.Equal(t, action.DefaultMaxRevisionsBack, cfg.Max())
}

func TestApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(c *Config)
		want   bool
	}{
		{name: "without the opt-in", change: func(*Config) {}, want: true},
		{name: "with the opt-in", change: func(c *Config) { c.AllowReadWrite = true }, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := validConfig()
			tt.change(&cfg)

			require.Equal(t, tt.want, action.ReadWriteBlocked(&cfg), "a read-write route may not list the config")

			api, _ := newFakeAPI(t)
			require.Equal(t, tt.want, action.ReadWriteBlocked(testRollback(t, api, tt.change)), "the built action reports it")
		})
	}
}

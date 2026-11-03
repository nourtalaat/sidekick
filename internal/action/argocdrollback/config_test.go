package argocdrollback

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/registry"
)

var tokenRef = registry.SecretRef{Name: "argocd-rollback", Key: "token"}

// validConfig returns a config that passes Validate, for a test to change.
func validConfig() Config {
	return Config{
		BaseURL:     "https://argocd.example.com",
		TokenSecret: tokenRef,
		Limits:      Limits{Applications: argocd.Allowlist{"shop/*"}},
	}
}

func intPtr(n int) *int { return &n }

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		change  func(c *Config)
		wantErr string
	}{
		{name: "the smallest valid config", change: func(*Config) {}},
		{name: "a base url with a path", change: func(c *Config) { c.BaseURL = "https://example.com/argocd" }},
		{name: "a base url on a loopback host", change: func(c *Config) { c.BaseURL = "http://127.0.0.1:8080" }},
		{name: "a namespace", change: func(c *Config) { c.Namespace = "gitops" }},
		{name: "two applications", change: func(c *Config) { c.Limits.Applications = argocd.Allowlist{"shop/*", "platform/ingress-?"} }},
		{name: "maxRevisionsBack at the lowest", change: func(c *Config) { c.MaxRevisionsBack = intPtr(1) }},
		{name: "maxRevisionsBack at the highest", change: func(c *Config) { c.MaxRevisionsBack = intPtr(10) }},
		{name: "no base url", change: func(c *Config) { c.BaseURL = "" }, wantErr: "baseURL: required"},
		{name: "a base url over plain http", change: func(c *Config) { c.BaseURL = "http://argocd.example.com" }, wantErr: "baseURL: "},
		{name: "a base url with a login", change: func(c *Config) { c.BaseURL = "https://user:pass@argocd.example.com" }, wantErr: "baseURL: "},
		{name: "a base url that is not absolute", change: func(c *Config) { c.BaseURL = "argocd.example.com" }, wantErr: "baseURL: "},
		{name: "no token secret", change: func(c *Config) { c.TokenSecret = registry.SecretRef{} }, wantErr: "tokenSecret: required"},
		{name: "a namespace that is not one", change: func(c *Config) { c.Namespace = "Git_Ops" }, wantErr: "namespace: "},
		{name: "no applications", change: func(c *Config) { c.Limits.Applications = nil }, wantErr: "limits.applications: list at least one"},
		{name: "an application without a project", change: func(c *Config) { c.Limits.Applications = argocd.Allowlist{"checkout"} }, wantErr: "limits.applications[0]"},
		{name: "an application listed twice", change: func(c *Config) { c.Limits.Applications = argocd.Allowlist{"shop/a", "shop/a"} }, wantErr: `limits.applications[1]: "shop/a" is listed twice`},
		{name: "maxRevisionsBack of zero", change: func(c *Config) { c.MaxRevisionsBack = intPtr(0) }, wantErr: "maxRevisionsBack: 0 must be from 1 to 10"},
		{name: "maxRevisionsBack one over the highest", change: func(c *Config) { c.MaxRevisionsBack = intPtr(11) }, wantErr: "maxRevisionsBack: 11 must be from 1 to 10"},
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
baseURL: https://argocd.example.com
tokenSecret: { name: argocd-rollback, key: token }
namespace: gitops
allowReadWrite: true
maxRevisionsBack: 3
limits:
  applications: [shop/*, platform/ingress]
`

	var c Config

	require.NoError(t, yaml.Unmarshal([]byte(section), &c))
	require.NoError(t, c.Validate())
	require.Equal(t, Config{
		ReadWriteGate: action.ReadWriteGate{AllowReadWrite: true},
		RevisionLimit: action.RevisionLimit{MaxRevisionsBack: intPtr(3)},
		BaseURL:       "https://argocd.example.com",
		TokenSecret:   tokenRef,
		Namespace:     "gitops",
		Limits:        Limits{Applications: argocd.Allowlist{"shop/*", "platform/ingress"}},
	}, c)
}

func TestConfigIsApprovalOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		yaml string
		want bool
	}{
		{name: "without the opt-in", yaml: "baseURL: https://argocd.example.com", want: true},
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

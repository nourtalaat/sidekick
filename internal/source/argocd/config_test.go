package argocd

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := Config{BaseURL: "https://argocd.example.com", TokenSecret: testTokenRef, Applications: argo.Allowlist{"shop/*"}}

	tests := []struct {
		name string
		cfg  func(c *Config)
		want []string
	}{
		{name: "the smallest config", cfg: func(*Config) {}},
		{
			name: "every key",
			cfg: func(c *Config) {
				c.BaseURL = "https://example.com/argocd/"
				c.Applications = argo.Allowlist{"shop/*", "platform/ingress-?"}
				c.Namespace = "argo-cd"
				c.Settings = source.Settings{Timeout: 45 * time.Second}
			},
		},
		{name: "plain http on localhost", cfg: func(c *Config) { c.BaseURL = "http://127.0.0.1:8080" }},
		{
			name: "nothing set",
			cfg:  func(c *Config) { *c = Config{} },
			want: []string{"tokenSecret: required", "baseURL: required", "applications: list at least one"},
		},
		{
			name: "a plain http base URL on the network",
			cfg:  func(c *Config) { c.BaseURL = "http://argocd.example.com" },
			want: []string{`baseURL: "http://argocd.example.com" must use https; plain http is only allowed for localhost`},
		},
		{
			name: "a base URL that is not absolute",
			cfg:  func(c *Config) { c.BaseURL = "argocd.example.com" },
			want: []string{`baseURL: "argocd.example.com" must be an absolute https URL`},
		},
		{
			name: "a base URL with user info",
			cfg:  func(c *Config) { c.BaseURL = "https://admin:secret@argocd.example.com" },
			want: []string{"must be an absolute https URL"},
		},
		{
			name: "a bare application name",
			cfg:  func(c *Config) { c.Applications = argo.Allowlist{"checkout"} },
			want: []string{`applications[0]: "checkout" must have the form project/name`},
		},
		{
			name: "an application glob that does not parse",
			cfg:  func(c *Config) { c.Applications = argo.Allowlist{"shop/*", "shop/[a"} },
			want: []string{`applications[1]: invalid glob "shop/[a"`},
		},
		{
			name: "a namespace that is not one",
			cfg:  func(c *Config) { c.Namespace = "Argo CD" },
			want: []string{`namespace: "Argo CD" is not a Kubernetes namespace name`},
		},
		{
			name: "a timeout longer than the HTTP client waits",
			cfg:  func(c *Config) { c.Settings = source.Settings{Timeout: 2 * time.Minute} },
			want: []string{"timeout: 2m0s is above"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid
			tt.cfg(&cfg)

			err := cfg.Validate()
			if len(tt.want) == 0 {
				require.NoError(t, err)

				return
			}

			for _, want := range tt.want {
				require.ErrorContains(t, err, want)
			}
		})
	}
}

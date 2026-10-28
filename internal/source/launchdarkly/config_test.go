package launchdarkly

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the smallest config", cfg: Config{TokenSecret: testTokenRef, Projects: testProjects}},
		{
			name: "every key",
			cfg: Config{
				TokenSecret: testTokenRef,
				Projects:    flags.Allowlist{{Key: "shop", Environments: []string{"production"}}, {Key: "Billing_2", Environments: []string{"prod.eu"}}},
				BaseURL:     "https://app.eu.launchdarkly.com/",
				Settings:    source.Settings{Timeout: 45 * time.Second},
			},
		},
		{name: "plain http on localhost", cfg: Config{TokenSecret: testTokenRef, Projects: testProjects, BaseURL: "http://127.0.0.1:8080"}},
		{name: "nothing set", want: []string{"tokenSecret: required", "projects: list at least one project"}},
		{
			name: "keys that could widen a resource specifier",
			cfg:  Config{TokenSecret: testTokenRef, Projects: flags.Allowlist{{Key: "*", Environments: []string{"prod:flag/x"}}}},
			want: []string{`projects[0].key: "*" is not a project key`, `projects[0].environments[0]: "prod:flag/x" is not an environment key`},
		},
		{
			name: "a plain http base URL on the network",
			cfg:  Config{TokenSecret: testTokenRef, Projects: testProjects, BaseURL: "http://app.launchdarkly.com"},
			want: []string{`baseURL: "http://app.launchdarkly.com" must use https; plain http is only allowed for localhost`},
		},
		{
			name: "a base URL that is not absolute",
			cfg:  Config{TokenSecret: testTokenRef, Projects: testProjects, BaseURL: "app.eu.launchdarkly.com"},
			want: []string{`baseURL: "app.eu.launchdarkly.com" must be an absolute https URL`},
		},
		{
			name: "a base URL with a query",
			cfg:  Config{TokenSecret: testTokenRef, Projects: testProjects, BaseURL: "https://app.launchdarkly.com?x=1"},
			want: []string{`must be an absolute https URL`},
		},
		{
			name: "a timeout longer than the HTTP client waits",
			cfg:  Config{TokenSecret: testTokenRef, Projects: testProjects, Settings: source.Settings{Timeout: 2 * time.Minute}},
			want: []string{"timeout: 2m0s is above"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.cfg.Validate()
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

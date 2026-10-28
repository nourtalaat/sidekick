package unleash

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
		{name: "the smallest config", cfg: Config{BaseURL: appURL, TokenSecret: testTokenRef, Projects: testProjects}},
		{
			name: "every key",
			cfg: Config{
				BaseURL:     "https://unleash.example.com/unleash/",
				TokenSecret: testTokenRef,
				Projects:    flags.Allowlist{{Key: "default", Environments: []string{"production"}}, {Key: "Billing_2", Environments: []string{"prod.eu"}}},
				Settings:    source.Settings{Timeout: 45 * time.Second},
			},
		},
		{name: "plain http on localhost", cfg: Config{BaseURL: "http://127.0.0.1:4242", TokenSecret: testTokenRef, Projects: testProjects}},
		{
			name: "nothing set",
			want: []string{"baseURL: required", "tokenSecret: required", "projects: list at least one project"},
		},
		{
			name: "keys that could change a path or a filter",
			cfg: Config{BaseURL: appURL, TokenSecret: testTokenRef,
				Projects: flags.Allowlist{{Key: "..", Environments: []string{"prod,dev", "a/b"}}, {Key: "IS:x", Environments: []string{"production"}}}},
			want: []string{`projects[0].key: ".." is not a project key`, `projects[0].environments[0]: "prod,dev" is not an environment key`,
				`projects[0].environments[1]: "a/b" is not an environment key`, `projects[1].key: "IS:x" is not a project key`},
		},
		{
			name: "a project listed twice",
			cfg: Config{BaseURL: appURL, TokenSecret: testTokenRef,
				Projects: flags.Allowlist{{Key: "shop", Environments: []string{"production"}}, {Key: "shop", Environments: []string{"staging"}}}},
			want: []string{`projects[1].key: "shop" is listed twice`},
		},
		{
			name: "an environment listed twice and a project without any",
			cfg: Config{BaseURL: appURL, TokenSecret: testTokenRef,
				Projects: flags.Allowlist{{Key: "shop", Environments: []string{"production", "production"}}, {Key: "billing"}}},
			want: []string{`projects[0].environments[1]: "production" is listed twice`, "projects[1].environments: list at least one environment"},
		},
		{
			name: "a plain http base URL on the network",
			cfg:  Config{BaseURL: "http://unleash.example.com", TokenSecret: testTokenRef, Projects: testProjects},
			want: []string{`baseURL: "http://unleash.example.com" must use https; plain http is only allowed for localhost`},
		},
		{
			name: "a base URL that is not absolute",
			cfg:  Config{BaseURL: "unleash.example.com", TokenSecret: testTokenRef, Projects: testProjects},
			want: []string{`baseURL: "unleash.example.com" must be an absolute https URL`},
		},
		{
			name: "a base URL with a query",
			cfg:  Config{BaseURL: "https://unleash.example.com?x=1", TokenSecret: testTokenRef, Projects: testProjects},
			want: []string{`must be an absolute https URL`},
		},
		{
			name: "a base URL with credentials",
			cfg:  Config{BaseURL: "https://admin:secret@unleash.example.com", TokenSecret: testTokenRef, Projects: testProjects},
			want: []string{`must be an absolute https URL`},
		},
		{
			name: "a timeout longer than the HTTP client waits",
			cfg:  Config{BaseURL: appURL, TokenSecret: testTokenRef, Projects: testProjects, Settings: source.Settings{Timeout: 2 * time.Minute}},
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

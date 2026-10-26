package github

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	app := App{AppID: testAppID, InstallationID: testInstallationID, PrivateKeySecret: testKeyRef}

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the smallest config", cfg: Config{App: app, Repos: []string{"acme/checkout"}}},
		{
			name: "globs, services and an Enterprise Server",
			cfg: Config{
				App:          app,
				BaseURL:      "https://github.example.com/api/v3",
				Repos:        []string{"acme/*", "Platform/infra-?"},
				ServiceRepos: map[string]string{"checkout-api": "acme/checkout", "infra": "platform/INFRA-1"},
				Settings:     source.Settings{Timeout: 60 * time.Second},
			},
		},
		{
			name: "nothing set",
			want: []string{
				"app.appID: required",
				"app.installationID: required",
				"app.privateKeySecret: required",
				"repos: list at least one repository or owner/name glob, such as acme/*",
			},
		},
		{
			name: "a negative app ID",
			cfg:  Config{App: App{AppID: -1, InstallationID: 1, PrivateKeySecret: registry.SecretRef{Name: "a", Key: "b"}}, Repos: []string{"acme/x"}},
			want: []string{"app.appID: required"},
		},
		{
			name: "a base URL that is not absolute",
			cfg:  Config{App: app, Repos: []string{"acme/x"}, BaseURL: "github.example.com/api/v3"},
			want: []string{`baseURL: "github.example.com/api/v3" must be an absolute https URL`},
		},
		{
			name: "a base URL with another scheme",
			cfg:  Config{App: app, Repos: []string{"acme/x"}, BaseURL: "ftp://github.example.com/"},
			want: []string{`baseURL: "ftp://github.example.com/" must be an absolute https URL`},
		},
		{
			name: "a plain http base URL on the network",
			cfg:  Config{App: app, Repos: []string{"acme/x"}, BaseURL: "http://github.example.com/api/v3/"},
			want: []string{`baseURL: "http://github.example.com/api/v3/" must use https; plain http is only allowed for localhost`},
		},
		{
			name: "a plain http base URL on an address that only looks local",
			cfg:  Config{App: app, Repos: []string{"acme/x"}, BaseURL: "http://localhost.example.com/"},
			want: []string{`baseURL: "http://localhost.example.com/" must use https; plain http is only allowed for localhost`},
		},
		{name: "plain http on localhost", cfg: Config{App: app, Repos: []string{"acme/x"}, BaseURL: "http://localhost:8080/"}},
		{name: "plain http on 127.0.0.0/8", cfg: Config{App: app, Repos: []string{"acme/x"}, BaseURL: "http://127.0.0.2:8080/"}},
		{name: "plain http on ::1", cfg: Config{App: app, Repos: []string{"acme/x"}, BaseURL: "http://[::1]:8080/"}},
		{
			name: "a timeout above the HTTP client's limit",
			cfg:  Config{App: app, Repos: []string{"acme/x"}, Settings: source.Settings{Timeout: 61 * time.Second}},
			want: []string{"timeout: 1m1s is above 1m0s, the longest the HTTP client waits for response headers"},
		},
		{
			name: "bad repos entries",
			cfg:  Config{App: app, Repos: []string{"acme", "acme/x/y", "acme/[x", "*/*"}},
			want: []string{
				`repos[0]: "acme" must have the form owner/name, with optional wildcards`,
				`repos[1]: "acme/x/y" must have the form owner/name, with optional wildcards`,
				`repos[2]: invalid glob "acme/[x": syntax error in pattern`,
			},
		},
		{
			name: "bad service repositories",
			cfg: Config{
				App:          app,
				Repos:        []string{"acme/*"},
				ServiceRepos: map[string]string{"billing": "other/billing", "checkout": "acme/*", " ": "acme/x", "up": "acme/.."},
			},
			want: []string{
				"serviceRepos: a service name must not be empty",
				`serviceRepos.billing: "other/billing" is not in repos`,
				`serviceRepos.checkout: "acme/*" must have the form owner/name`,
				`serviceRepos.up: "acme/.." must have the form owner/name`,
			},
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

			require.EqualError(t, err, strings.Join(tt.want, "\n"))
		})
	}
}

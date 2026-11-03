package gitlab

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

	token := registry.SecretRef{Name: "sidekick-gitlab", Key: "token"}

	tests := []struct {
		name string
		cfg  Config
		want []string
	}{
		{name: "the smallest config", cfg: Config{TokenSecret: token, Projects: []string{"acme/checkout"}}},
		{
			name: "globs, services and a self-managed GitLab under a sub-path",
			cfg: Config{
				TokenSecret:     token,
				BaseURL:         "https://example.com/gitlab/",
				Projects:        []string{"acme/*", "Platform/infra/**", "tools/ci-?", "**"},
				ServiceProjects: map[string]string{"checkout-api": "acme/checkout", "infra": "platform/INFRA/team-a/repo"},
				Settings:        source.Settings{Timeout: 60 * time.Second},
			},
		},
		{
			name: "nothing set",
			want: []string{
				"tokenSecret: required, as { name, key }",
				"projects: list at least one project path or glob, such as acme/*",
			},
		},
		{
			name: "a token secret without a key",
			cfg:  Config{TokenSecret: registry.SecretRef{Name: "a"}, Projects: []string{"acme/x"}},
			want: []string{"tokenSecret: required, as { name, key }"},
		},
		{
			name: "a base URL that is not absolute",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "gitlab.example.com"},
			want: []string{`baseURL: "gitlab.example.com" must be an absolute https URL, such as https://gitlab.example.com`},
		},
		{
			name: "a base URL with another scheme",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "ftp://gitlab.example.com/"},
			want: []string{`baseURL: "ftp://gitlab.example.com/" must be an absolute https URL, such as https://gitlab.example.com`},
		},
		{
			name: "a base URL with a query",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "https://gitlab.example.com/?a=b"},
			want: []string{`baseURL: "https://gitlab.example.com/?a=b" must be an absolute https URL, such as https://gitlab.example.com`},
		},
		{
			name: "a base URL with credentials",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "https://me:pw@gitlab.example.com/"},
			want: []string{`baseURL: "https://me:pw@gitlab.example.com/" must be an absolute https URL, such as https://gitlab.example.com`},
		},
		{
			name: "a base URL that is the API root",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "https://gitlab.example.com/api/v4/"},
			want: []string{`baseURL: "https://gitlab.example.com/api/v4/" must be the root of the GitLab, without /api/v4`},
		},
		{
			name: "a plain http base URL on the network",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "http://gitlab.example.com"},
			want: []string{`baseURL: "http://gitlab.example.com" must use https; plain http is only allowed for localhost`},
		},
		{
			name: "a plain http base URL on an address that only looks local",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "http://localhost.example.com/"},
			want: []string{`baseURL: "http://localhost.example.com/" must use https; plain http is only allowed for localhost`},
		},
		{name: "plain http on localhost", cfg: Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "http://localhost:8080"}},
		{name: "plain http on 127.0.0.0/8", cfg: Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "http://127.0.0.2:8080"}},
		{name: "plain http on ::1", cfg: Config{TokenSecret: token, Projects: []string{"acme/x"}, BaseURL: "http://[::1]:8080"}},
		{
			name: "a timeout above the HTTP client's limit",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme/x"}, Settings: source.Settings{Timeout: 61 * time.Second}},
			want: []string{"timeout: 1m1s is above 1m0s, the longest the HTTP client waits for response headers"},
		},
		{
			name: "bad projects entries",
			cfg:  Config{TokenSecret: token, Projects: []string{"acme", "acme//x", "/acme/x", "acme/[x", "acme/a**", "acme/a b", ""}},
			want: []string{
				`projects[0]: "acme" must be a path such as group/project, with optional wildcards`,
				`projects[1]: "acme//x" must be a path such as group/project, without an empty part`,
				`projects[2]: "/acme/x" must be a path such as group/project, without an empty part`,
				`projects[3]: invalid glob "acme/[x": syntax error in pattern`,
				`projects[4]: "acme/a**": ** must be a whole part of the path`,
				`projects[5]: "acme/a b" must be a path such as group/project, with optional wildcards`,
				`projects[6]: required`,
			},
		},
		{
			name: "bad service projects",
			cfg: Config{
				TokenSecret: token,
				Projects:    []string{"acme/*"},
				ServiceProjects: map[string]string{
					"billing": "other/billing", "checkout": "acme/*", " ": "acme/x", "up": "acme/..", "single": "acme", "id": "42",
				},
			},
			want: []string{
				"serviceProjects: a service name must not be empty",
				`serviceProjects.billing: "other/billing" is not in projects`,
				`serviceProjects.checkout: "acme/*" must be a full project path such as group/project`,
				`serviceProjects.id: "42" must be a full project path such as group/project`,
				`serviceProjects.single: "acme" must be a full project path such as group/project`,
				`serviceProjects.up: "acme/.." must be a full project path such as group/project`,
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

func TestConfigAllows(t *testing.T) {
	t.Parallel()

	cfg := Config{Projects: []string{"acme/checkout", "acme/platform/*", "infra/**"}}

	tests := []struct {
		name    string
		project string
		want    bool
	}{
		{name: "an exact path", project: "acme/checkout", want: true},
		{name: "an exact path in another case", project: "Acme/Checkout", want: true},
		{name: "a project of a subgroup glob", project: "acme/platform/api", want: true},
		{name: "a deeper project of a one-level glob", project: "acme/platform/team/api", want: false},
		{name: "a project of a deep glob", project: "infra/a/b/c/d", want: true},
		{name: "a project directly in a deep glob", project: "infra/x", want: true},
		{name: "another group", project: "other/checkout", want: false},
		{name: "a longer name that starts like an entry", project: "acme/checkout-api", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, cfg.allows(tt.project))
		})
	}
}

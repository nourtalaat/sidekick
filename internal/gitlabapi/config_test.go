package gitlabapi

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		baseURL string
		secrets bool
		wantAPI string
		wantErr error
	}{
		{name: "gitlab.com by default", secrets: true, wantAPI: "https://gitlab.com/api/v4"},
		{name: "a self-managed GitLab under a sub-path", baseURL: "https://example.com/gitlab/", secrets: true, wantAPI: "https://example.com/gitlab/api/v4"},
		{name: "no secrets resolver", wantErr: errNoSecrets},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			opts := Options{BaseURL: tt.baseURL, Token: testTokenRef}
			if tt.secrets {
				opts.Secrets = registry.NewMockSecrets(gomock.NewController(t))
			}

			c, err := New(opts)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantAPI, c.apiURL)
		})
	}
}

func TestCheckBaseURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "empty means gitlab.com", raw: ""},
		{name: "https root", raw: "https://gitlab.example.com"},
		{name: "https under a sub-path", raw: "https://example.com/gitlab/"},
		{name: "plain http on localhost", raw: "http://localhost:8080"},
		{name: "plain http on 127.0.0.0/8", raw: "http://127.0.0.2:8080"},
		{name: "plain http on ::1", raw: "http://[::1]:8080"},
		{name: "not absolute", raw: "gitlab.example.com", want: `baseURL: "gitlab.example.com" must be an absolute https URL, such as https://gitlab.example.com`},
		{name: "another scheme", raw: "ftp://gitlab.example.com/", want: `baseURL: "ftp://gitlab.example.com/" must be an absolute https URL, such as https://gitlab.example.com`},
		{name: "a query", raw: "https://gitlab.example.com/?a=b", want: `baseURL: "https://gitlab.example.com/?a=b" must be an absolute https URL, such as https://gitlab.example.com`},
		{name: "a user name", raw: "https://me@gitlab.example.com/", want: `baseURL: "https://me@gitlab.example.com/" must be an absolute https URL, such as https://gitlab.example.com`},
		{name: "the API root", raw: "https://gitlab.example.com/api/v4/", want: `baseURL: "https://gitlab.example.com/api/v4/" must be the root of the GitLab, without /api/v4`},
		{name: "plain http on the network", raw: "http://gitlab.example.com", want: `baseURL: "http://gitlab.example.com" must use https; plain http is only allowed for localhost`},
		{name: "plain http on a name that only looks local", raw: "http://localhost.example.com/", want: `baseURL: "http://localhost.example.com/" must use https; plain http is only allowed for localhost`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := CheckBaseURL(tt.raw)
			if tt.want == "" {
				require.NoError(t, err)

				return
			}

			require.EqualError(t, err, tt.want)
		})
	}
}

func TestBaseURL(t *testing.T) {
	t.Parallel()

	require.Equal(t, defaultBaseURL, BaseURL(""))
	require.Equal(t, "https://example.com/gitlab", BaseURL("https://example.com/gitlab/"))
	require.Equal(t, "https://gitlab.example.com", BaseURL("https://gitlab.example.com"))
}

func TestValidProject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		project string
		want    bool
	}{
		{project: "acme/checkout", want: true},
		{project: "acme/platform/team/checkout", want: true},
		{project: "acme.io/a_b-c", want: true},
		{project: "acme"},
		{project: "42"},
		{project: "acme/"},
		{project: "/acme/x"},
		{project: "acme//x"},
		{project: "acme/.."},
		{project: "./acme"},
		{project: "acme/x*"},
		{project: "acme/a b"},
		{project: "a/b?c=d"},
	}

	for _, tt := range tests {
		t.Run(tt.project, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, ValidProject(tt.project))
		})
	}
}

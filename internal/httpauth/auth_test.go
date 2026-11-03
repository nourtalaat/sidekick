package httpauth_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/httpauth"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestAuthApply(t *testing.T) {
	t.Parallel()

	errRead := errors.New("no such secret")

	tests := []struct {
		name       string
		cfg        httpauth.Config
		secret     string
		secretErr  error
		wantAuth   string
		wantBasic  [2]string
		wantHeader string
		wantErr    string
	}{
		{name: "no credential", cfg: httpauth.Config{Headers: map[string]string{"X-Scope-OrgID": "tenant-a"}}, wantHeader: "tenant-a"},
		{name: "a bearer token, trimmed", cfg: httpauth.Config{BearerTokenSecret: ref("token")}, secret: " abc\n", wantAuth: "Bearer abc"},
		{
			name: "basic auth, trailing newline cut",
			cfg:  httpauth.Config{BasicAuth: &httpauth.BasicAuth{Username: "sidekick", PasswordSecret: *ref("password")}}, secret: "pw\n",
			wantBasic: [2]string{"sidekick", "pw"},
		},
		{name: "an empty token", cfg: httpauth.Config{BearerTokenSecret: ref("token")}, secret: "  \n", wantErr: "the secret is empty"},
		{name: "a secret that cannot be read", cfg: httpauth.Config{BearerTokenSecret: ref("token")}, secretErr: errRead, wantErr: "no such secret"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var secrets registry.Secrets

			if tt.cfg.UsesSecrets() {
				m := registry.NewMockSecrets(gomock.NewController(t))
				m.EXPECT().Resolve(gomock.Any(), gomock.Any()).Return([]byte(tt.secret), tt.secretErr)
				secrets = m
			}

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://am.example.com", http.NoBody)
			require.NoError(t, err)

			err = httpauth.NewAuth(tt.cfg, secrets).Apply(t.Context(), req)

			if tt.wantErr != "" {
				require.ErrorContains(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)

			if tt.wantAuth != "" {
				require.Equal(t, tt.wantAuth, req.Header.Get("Authorization"))
			}

			if tt.wantBasic != ([2]string{}) {
				user, password, ok := req.BasicAuth()
				require.True(t, ok)
				require.Equal(t, tt.wantBasic, [2]string{user, password})
			}

			if tt.wantHeader != "" {
				require.Equal(t, tt.wantHeader, req.Header.Get("X-Scope-OrgID"))
			}
		})
	}
}

func TestWrapTransport(t *testing.T) {
	t.Parallel()

	t.Run("no tls leaves the transport as it is", func(t *testing.T) {
		t.Parallel()

		next := http.DefaultTransport

		got, err := httpauth.WrapTransport(next, nil, nil)
		require.NoError(t, err)
		require.Same(t, next, got)
	})

	t.Run("tls needs an http.Transport", func(t *testing.T) {
		t.Parallel()

		_, err := httpauth.WrapTransport(otherTransport{}, &httpauth.TLS{CASecret: ref("ca")}, nil)
		require.ErrorIs(t, err, httpauth.ErrNoTransport)
	})

	t.Run("tls wraps an http.Transport", func(t *testing.T) {
		t.Parallel()

		got, err := httpauth.WrapTransport(&http.Transport{}, &httpauth.TLS{CASecret: ref("ca")}, nil)
		require.NoError(t, err)
		require.NotNil(t, got)
	})
}

// otherTransport is a round tripper that is not an *http.Transport.
type otherTransport struct{}

var errNotCalled = errors.New("not called")

func (otherTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errNotCalled
}

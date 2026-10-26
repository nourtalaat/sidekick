package openai

import (
	"errors"
	"io/fs"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"go.yaml.in/yaml/v3"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  Config
		// noSecrets builds the connector without a secrets resolver.
		noSecrets bool
		// secretErr is what resolving the key returns.
		secretErr error
		wantErr   error
	}{
		{name: "OpenAI model with a key", cfg: Config{Settings: model.Settings{Model: "gpt-6-sol"}, APIKeySecret: keyRef}},
		{name: "local preset without a key or secrets", cfg: Config{Settings: model.Settings{Model: "qwen3.8:27b"}, BaseURL: ollamaURL}, noSecrets: true},
		{name: "key that cannot be read", cfg: Config{Settings: model.Settings{Model: "gpt-6-sol"}, APIKeySecret: keyRef}, secretErr: fs.ErrNotExist, wantErr: fs.ErrNotExist},
		{name: "key without secrets", cfg: Config{Settings: model.Settings{Model: "gpt-6-sol"}, APIKeySecret: keyRef}, noSecrets: true, wantErr: errNoSecrets},
		{name: "unknown model", cfg: Config{Settings: model.Settings{Model: "llama4:70b"}, BaseURL: ollamaURL}, wantErr: errUnknownModel},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			s := registry.NewMockSecrets(ctrl)

			if tt.cfg.APIKeySecret != (registry.SecretRef{}) && !tt.noSecrets {
				s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte(testAPIKey), tt.secretErr)
			}

			deps := registry.Deps{Secrets: s}
			if tt.noSecrets {
				deps.Secrets = nil
			}

			c, err := New(t.Context(), "gpt", tt.cfg, deps)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.cfg.Model, c.(*Connector).ModelID())
		})
	}
}

// TestKeyRotation checks that each call reads the API key again, so a
// rotated key is used without a restart.
func TestKeyRotation(t *testing.T) {
	t.Parallel()

	api := serve(t, responsesPath,
		recorded{status: http.StatusOK, file: "responses_text.sse"},
		recorded{status: http.StatusOK, file: "responses_text.sse"},
	)

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	gomock.InOrder(
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-1\n"), nil).Times(2),
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-2\n"), nil),
	)

	cfg := responsesConfig("gpt-6-sol")
	cfg.BaseURL = api.URL() + "/v1"

	c, err := New(t.Context(), "gpt", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
	require.NoError(t, err)

	for range 2 {
		_, err := c.Generate(t.Context(), userText("hi"))
		require.NoError(t, err)
	}

	sent := api.Requests()
	require.Equal(t, "Bearer key-1", sent[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer key-2", sent[1].Header.Get("Authorization"))
}

// TestGenerateKeyError checks that a key that cannot be read at call time
// is an auth error and sends nothing.
func TestGenerateKeyError(t *testing.T) {
	t.Parallel()

	api := fakehttp.New(t)

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	gomock.InOrder(
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-1"), nil),
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return(nil, errors.New("gone")),
	)

	cfg := responsesConfig("gpt-6-sol")
	cfg.BaseURL = api.URL() + "/v1"

	c, err := New(t.Context(), "gpt", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
	require.NoError(t, err)

	_, err = c.Generate(t.Context(), userText("hi"))
	requireKind(t, err, model.ErrAuth, 0)
	require.Empty(t, api.Requests())
}

// TestRegistered checks that the provider decodes and validates its section
// through the registry.
func TestRegistered(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		section string
		wantErr string
	}{
		{name: "valid", section: "model: qwen3.8:27b\nbaseURL: http://ollama:11434/v1\n"},
		{name: "invalid", section: "model: gpt-6-sol\n", wantErr: "apiKeySecret: required without baseURL"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var node yaml.Node
			require.NoError(t, yaml.Unmarshal([]byte(tt.section), &node))

			_, err := registry.Models.Check(t.Context(), Provider, "local", node.Content[0])
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

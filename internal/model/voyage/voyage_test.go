package voyage

import (
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testAPIKey is the fake API key the tests send. No test calls a real API.
const testAPIKey = "test-key-not-real"

// testDimensions is the length of the vectors in the recorded answers.
const testDimensions = 3

var keyRef = registry.SecretRef{Name: "sidekick-voyage", Key: "apiKey"}

// recorded is one recorded API answer, served from a file in fixture/.
type recorded struct {
	status  int
	file    string
	headers map[string]string
}

// serve starts a fake Voyage API of the test's own that answers calls to
// /v1/embeddings with the answers, in order.
func serve(t *testing.T, answers ...recorded) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)

	for _, a := range answers {
		headers := map[string]string{"Content-Type": "application/json"}
		for k, v := range a.headers {
			headers[k] = v
		}

		api.Handle(http.MethodPost, "/v1/embeddings", fakehttp.Response{
			Status:   a.status,
			Headers:  headers,
			BodyFile: filepath.Join("fixture", a.file),
		})
	}

	api.ExpectAllCalled()

	return api
}

// newTestConnector builds a connector that sends its requests to api.
func newTestConnector(t *testing.T, api *fakehttp.Server) *Connector {
	t.Helper()

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()

	cfg := Config{
		Settings:     model.Settings{Model: "voyage-4"},
		APIKeySecret: keyRef,
		BaseURL:      api.URL() + "/v1",
		Dimensions:   testDimensions,
	}

	c, err := New(t.Context(), "voyage", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
	require.NoError(t, err)

	conn, ok := c.(*Connector)
	require.True(t, ok)

	return conn
}

func TestEmbed(t *testing.T) {
	t.Parallel()

	texts := []string{"first alert", "second alert"}

	tests := []struct {
		name     string
		answer   recorded
		wantKind model.ErrorKind
		// wantRetryAfter is the wait the answer asks for.
		wantRetryAfter time.Duration
		wantErr        error
	}{
		{name: "vectors in the order of the texts", answer: recorded{status: http.StatusOK, file: "embeddings.json", headers: map[string]string{"X-Request-Id": "req-voyage"}}},
		{name: "vectors of the wrong length", answer: recorded{status: http.StatusOK, file: "wrong_length.json"}, wantKind: model.ErrInvalid, wantErr: errWrongVectorLength},
		{name: "fewer vectors than texts", answer: recorded{status: http.StatusOK, file: "missing.json"}, wantKind: model.ErrInvalid, wantErr: errBadAnswer},
		{name: "bad key", answer: recorded{status: http.StatusUnauthorized, file: "auth.json"}, wantKind: model.ErrAuth},
		{
			name:           "rate limited with retry-after",
			answer:         recorded{status: http.StatusTooManyRequests, file: "rate_limit.json", headers: map[string]string{"Retry-After": "3"}},
			wantKind:       model.ErrRateLimited,
			wantRetryAfter: 3 * time.Second,
		},
		{name: "invalid request", answer: recorded{status: http.StatusBadRequest, file: "invalid.json"}, wantKind: model.ErrInvalid},
		{name: "server error", answer: recorded{status: http.StatusServiceUnavailable, file: "server_error.json"}, wantKind: model.ErrOverloaded},
		{name: "gateway timeout", answer: recorded{status: http.StatusGatewayTimeout, file: "server_error.json"}, wantKind: model.ErrTimeout},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := serve(t, tt.answer)
			c := newTestConnector(t, api)

			require.Equal(t, testDimensions, c.Dimensions())
			require.Equal(t, "voyage-4", c.ModelID())

			got, err := c.Embed(t.Context(), texts)

			sent := api.Requests()
			require.Len(t, sent, 1)
			require.Equal(t, "Bearer "+testAPIKey, sent[0].Header.Get("Authorization"))

			var body map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(sent[0].Body, &body))
			require.JSONEq(t, `["first alert","second alert"]`, string(body["input"]))
			require.JSONEq(t, `"voyage-4"`, string(body["model"]))
			require.JSONEq(t, `3`, string(body["output_dimension"]))
			require.NotContains(t, body, "input_type")

			if tt.wantKind != "" {
				var merr *model.Error
				require.ErrorAs(t, err, &merr)
				require.Equal(t, tt.wantKind, merr.Kind)
				require.Equal(t, tt.wantRetryAfter, merr.RetryAfter)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				}

				return
			}

			require.NoError(t, err)
			require.Equal(t, [][]float32{{1, 0.75, 0.0625}, {0.5, 0.25, -0.125}}, got.Vectors)
			require.Equal(t, int64(11), got.Usage.InputTokens)
			require.Equal(t, "req-voyage", got.RequestID)
		})
	}
}

func TestGenerate(t *testing.T) {
	t.Parallel()

	c := newTestConnector(t, serve(t))

	_, err := c.Generate(t.Context(), model.Request{})

	var merr *model.Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, model.ErrInvalid, merr.Kind)
	require.ErrorIs(t, err, errGenerate)
	require.Equal(t, model.Capabilities{}, c.Capabilities())
}

func TestNew(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		noSecrets bool
		secretErr error
		wantErr   error
	}{
		{name: "key that can be read"},
		{name: "key that cannot be read", secretErr: fs.ErrNotExist, wantErr: fs.ErrNotExist},
		{name: "no secrets resolver", noSecrets: true, wantErr: errNoSecrets},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctrl := gomock.NewController(t)
			s := registry.NewMockSecrets(ctrl)

			deps := registry.Deps{Secrets: s}
			if tt.noSecrets {
				deps.Secrets = nil
			} else {
				s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte(testAPIKey), tt.secretErr)
			}

			_, err := New(t.Context(), "voyage", Config{Settings: model.Settings{Model: "voyage-4"}, APIKeySecret: keyRef, Dimensions: 1024}, deps)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestKeyRotation(t *testing.T) {
	t.Parallel()

	api := serve(t,
		recorded{status: http.StatusOK, file: "missing.json"},
		recorded{status: http.StatusOK, file: "missing.json"},
	)

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	gomock.InOrder(
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-1\n"), nil).Times(2),
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key-2\n"), nil),
	)

	cfg := Config{Settings: model.Settings{Model: "voyage-4"}, APIKeySecret: keyRef, BaseURL: api.URL() + "/v1", Dimensions: testDimensions}

	c, err := New(t.Context(), "voyage", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
	require.NoError(t, err)

	embedder, ok := c.(model.Embedder)
	require.True(t, ok)

	// The answers hold one vector, so a call with one text succeeds.
	for range 2 {
		_, err := embedder.Embed(t.Context(), []string{"a"})
		require.NoError(t, err)
	}

	sent := api.Requests()
	require.Len(t, sent, 2)
	require.Equal(t, "Bearer key-1", sent[0].Header.Get("Authorization"))
	require.Equal(t, "Bearer key-2", sent[1].Header.Get("Authorization"))
}

func TestKeyThatCannotBeRead(t *testing.T) {
	t.Parallel()

	api := serve(t)

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	gomock.InOrder(
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte("key"), nil),
		s.EXPECT().Resolve(gomock.Any(), keyRef).Return(nil, errors.New("gone")),
	)

	cfg := Config{Settings: model.Settings{Model: "voyage-4"}, APIKeySecret: keyRef, BaseURL: api.URL() + "/v1", Dimensions: testDimensions}

	c, err := New(t.Context(), "voyage", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
	require.NoError(t, err)

	embedder, ok := c.(model.Embedder)
	require.True(t, ok)

	_, err = embedder.Embed(t.Context(), []string{"a"})

	var merr *model.Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, model.ErrAuth, merr.Kind)
	require.Empty(t, api.Requests())
}

func TestConfigValidate(t *testing.T) {
	t.Parallel()

	valid := func() Config {
		return Config{Settings: model.Settings{Model: "voyage-4"}, APIKeySecret: keyRef, Dimensions: 1024}
	}

	tests := []struct {
		name string
		edit func(*Config)
		// wantErr is a text the error holds; empty means a valid config.
		wantErr string
	}{
		{name: "valid"},
		{name: "valid with another length and model", edit: func(c *Config) { c.Model = "voyage-3.5-lite"; c.Dimensions = 256 }},
		{name: "valid with a loopback proxy over http", edit: func(c *Config) { c.BaseURL = "http://localhost:8080/v1" }},
		{name: "no key", edit: func(c *Config) { c.APIKeySecret = registry.SecretRef{} }, wantErr: "apiKeySecret: required"},
		{name: "no model", edit: func(c *Config) { c.Model = "" }, wantErr: "model: required"},
		{name: "unknown model", edit: func(c *Config) { c.Model = "voyage-9" }, wantErr: `model: "voyage-9" is not a Voyage embedding model`},
		{name: "no dimensions", edit: func(c *Config) { c.Dimensions = 0 }, wantErr: "dimensions: required"},
		{name: "a length the model does not return", edit: func(c *Config) { c.Dimensions = 1536 }, wantErr: "dimensions: 1536 is not a length"},
		{name: "plain http to another host", edit: func(c *Config) { c.BaseURL = "http://proxy.internal/v1" }, wantErr: "must use https"},
		{name: "a base URL that is not absolute", edit: func(c *Config) { c.BaseURL = "proxy/v1" }, wantErr: "must be an absolute https URL"},
		{name: "generation settings", edit: func(c *Config) { c.MaxTokens = 10 }, wantErr: "maxTokens: not used for embeddings"},
		{name: "negative retries", edit: func(c *Config) { c.Retries = -1 }, wantErr: "retries: must not be negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := valid()
			if tt.edit != nil {
				tt.edit(&cfg)
			}

			dims, ok := cfg.Embedding()
			require.True(t, ok)
			require.Equal(t, cfg.Dimensions, dims)

			err := cfg.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)

				return
			}

			require.ErrorContains(t, err, tt.wantErr)
		})
	}
}

func TestRegistered(t *testing.T) {
	t.Parallel()

	require.Contains(t, registry.Models.Types(), Provider)
}

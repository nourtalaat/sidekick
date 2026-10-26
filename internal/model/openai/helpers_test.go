package openai

import (
	"encoding/json"
	"maps"
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

// Paths of the two APIs under the fake server's /v1 base URL.
const (
	responsesPath = "/v1/responses"
	chatPath      = "/v1/chat/completions"
)

// Content types of recorded responses.
const (
	contentTypeSSE  = "text/event-stream"
	contentTypeJSON = "application/json"
)

// recorded is one recorded API response, served from a file in fixture/.
type recorded struct {
	status  int
	file    string
	headers map[string]string
}

// serve starts a fake API of the test's own that answers calls to path
// with the responses, in order. The test fails if a response is left
// unused.
func serve(t *testing.T, path string, responses ...recorded) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)

	for _, r := range responses {
		contentType := contentTypeSSE
		if r.status != http.StatusOK {
			contentType = contentTypeJSON
		}

		headers := map[string]string{"Content-Type": contentType}
		maps.Copy(headers, r.headers)

		api.Handle(http.MethodPost, path, fakehttp.Response{
			Status:   r.status,
			Headers:  headers,
			BodyFile: filepath.Join("fixture", r.file),
		})
	}

	api.ExpectAllCalled()

	return api
}

// newTestConnector builds a connector from cfg that sends its requests to
// api. The base URL is the fake server's, and the API key is resolved
// through a mock when cfg names one.
func newTestConnector(t *testing.T, api *fakehttp.Server, cfg Config) *Connector {
	t.Helper()

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)

	if cfg.APIKeySecret != (registry.SecretRef{}) {
		s.EXPECT().Resolve(gomock.Any(), cfg.APIKeySecret).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()
	}

	cfg.BaseURL = api.URL() + "/v1"

	c, err := New(t.Context(), "gpt", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
	require.NoError(t, err)

	conn, ok := c.(*Connector)
	require.True(t, ok)

	return conn
}

// responsesConfig is a Responses API connector for modelID with a key.
func responsesConfig(modelID string) Config {
	return Config{Settings: model.Settings{Model: modelID}, APIKeySecret: keyRef, API: APIResponses}
}

// requestBody returns the JSON body of request i that api received, decoded.
func requestBody(t *testing.T, api *fakehttp.Server, i int) map[string]json.RawMessage {
	t.Helper()

	sent := api.Requests()
	require.Greater(t, len(sent), i, "request %d was not sent", i)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(sent[i].Body, &m))

	return m
}

// userText is a request with one user message.
func userText(text string) model.Request {
	return model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: text}}}}}
}

// requireKind checks that err is a *model.Error of kind that asks to wait
// retryAfter.
func requireKind(t *testing.T, err error, kind model.ErrorKind, retryAfter time.Duration) {
	t.Helper()

	var merr *model.Error
	require.ErrorAs(t, err, &merr)
	require.Equal(t, kind, merr.Kind, "error: %v", err)
	require.Equal(t, retryAfter, merr.RetryAfter)
}

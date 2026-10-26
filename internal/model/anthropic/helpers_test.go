package anthropic

import (
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testAPIKey is the fake API key the tests send. No test calls the real API.
const testAPIKey = "test-key-not-real"

// messagesPath is the path of the Messages API.
const messagesPath = "/v1/messages"

// Content types of recorded responses.
const (
	contentTypeSSE  = "text/event-stream"
	contentTypeJSON = "application/json"
)

// apiKeyRef is the secret reference every test connector uses.
var apiKeyRef = registry.SecretRef{Name: "sidekick-anthropic", Key: "apiKey"}

// recorded is one recorded API response, served from a file in fixture/.
type recorded struct {
	status  int
	file    string
	headers map[string]string
}

// serve starts a fake API of the test's own that answers calls to the
// Messages API with the responses, in order. The test fails if a response
// is left unused.
func serve(t *testing.T, responses ...recorded) *fakehttp.Server {
	t.Helper()

	api := fakehttp.New(t)

	for _, r := range responses {
		contentType := contentTypeSSE
		if r.status != http.StatusOK {
			contentType = contentTypeJSON
		}

		headers := map[string]string{"Content-Type": contentType}
		maps.Copy(headers, r.headers)

		api.Handle(http.MethodPost, messagesPath, fakehttp.Response{
			Status:   r.status,
			Headers:  headers,
			BodyFile: filepath.Join("fixture", r.file),
		})
	}

	api.ExpectAllCalled()

	return api
}

// newTestConnector builds a connector for modelID that sends its requests
// to api. edit, when set, changes the config first.
func newTestConnector(t *testing.T, api *fakehttp.Server, modelID string, edit func(*Config)) *Connector {
	t.Helper()

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	s.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()

	cfg := Config{
		Settings:     model.Settings{Model: modelID},
		APIKeySecret: apiKeyRef,
		BaseURL:      api.URL(),
	}

	if edit != nil {
		edit(&cfg)
	}

	c, err := New(t.Context(), "claude", cfg, registry.Deps{Secrets: s, HTTP: api.Client()})
	require.NoError(t, err)

	conn, ok := c.(*Connector)
	require.True(t, ok)

	return conn
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

// sentKey returns the API key headers of a request, joined.
func sentKey(r fakehttp.Request) string {
	return r.Header.Get("Authorization") + r.Header.Get("X-Api-Key")
}

// userText is a request with one user message.
func userText(text string) model.Request {
	return model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: text}}}}}
}

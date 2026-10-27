package google

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"cloud.google.com/go/auth"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Fake credentials the tests send. No test calls a real API.
const (
	testAPIKey       = "test-key-not-real"
	testAccessToken  = "test-token-not-real"
	testProject      = "sidekick-test"
	testQuotaProject = "sidekick-quota"
)

// Paths of a streamed generateContent call of flash on each backend.
const (
	flash      = "gemini-3.8-flash"
	geminiPath = "/v1beta/models/" + flash + ":streamGenerateContent"
	vertexPath = "/v1beta1/projects/" + testProject + "/locations/global/publishers/google/models/" + flash + ":streamGenerateContent"
)

// Content types of recorded responses.
const (
	contentTypeSSE  = "text/event-stream"
	contentTypeJSON = "application/json"
)

// keyRef is the secret reference of the Gemini API key in the tests.
var keyRef = registry.SecretRef{Name: "sidekick-gemini", Key: "apiKey"}

// recorded is one recorded API response, served from a file in fixture/.
type recorded struct {
	status  int
	file    string
	headers map[string]string
}

// fixedToken is a token source that always gives the same token, or err.
type fixedToken struct {
	err error
}

// Token implements tokenSource.
func (f fixedToken) Token(context.Context) (*auth.Token, error) {
	if f.err != nil {
		return nil, f.err
	}

	return &auth.Token{Value: testAccessToken, Type: "Bearer", Expiry: time.Now().Add(time.Hour)}, nil
}

// serve starts a fake API of the test's own that answers POST calls to
// path with the responses, in order. The test fails if a response is left
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
// api. A gemini connector reads its key through a mock; a vertex one gets a
// fixed token instead of the Application Default Credentials.
func newTestConnector(t *testing.T, api *fakehttp.Server, cfg Config) *Connector {
	t.Helper()

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	s.EXPECT().Resolve(gomock.Any(), keyRef).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()

	cfg.BaseURL = api.URL() + "/"

	c, err := build(t.Context(), "gemini", cfg, registry.Deps{Secrets: s, HTTP: api.Client()}, fixedCredentials(nil))
	require.NoError(t, err)

	return c
}

// fixedCredentials returns a credential finder that gives fixedToken, or
// fails with err.
func fixedCredentials(err error) credentialFinder {
	return func(context.Context, *http.Client) (tokenSource, string, error) {
		if err != nil {
			return nil, "", err
		}

		return fixedToken{}, testQuotaProject, nil
	}
}

// geminiConfig is a gemini backend connector for modelID with a key.
func geminiConfig(modelID string) Config {
	return Config{Settings: model.Settings{Model: modelID}, APIKeySecret: keyRef}
}

// vertexConfig is a vertex backend connector for modelID in the global
// location.
func vertexConfig(modelID string) Config {
	return Config{Settings: model.Settings{Model: modelID}, Backend: BackendVertex, Project: testProject}
}

// requestBody returns the JSON body of request i that api received, decoded.
func requestBody(t *testing.T, api *fakehttp.Server, i int) map[string]any {
	t.Helper()

	sent := api.Requests()
	require.Greater(t, len(sent), i, "request %d was not sent", i)

	var m map[string]any
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

// changed returns cfg after change.
func changed(cfg Config, change func(c *Config)) Config {
	change(&cfg)

	return cfg
}

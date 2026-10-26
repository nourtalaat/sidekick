package anthropic

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"sync"
	"testing"

	"github.com/h2non/gock"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

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

// recorded is one recorded API response served by gock.
type recorded struct {
	status  int
	file    string
	headers map[string]string
}

// requests collects the requests a mock received.
type requests struct {
	mu     sync.Mutex
	bodies [][]byte
	auth   []string
}

// body returns the JSON body of request i, decoded.
func (r *requests) body(t *testing.T, i int) map[string]json.RawMessage {
	t.Helper()

	r.mu.Lock()
	defer r.mu.Unlock()

	require.Greater(t, len(r.bodies), i, "request %d was not sent", i)

	var m map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(r.bodies[i], &m))

	return m
}

// capture is a gock matcher that records each request it sees.
func (r *requests) capture(req *http.Request, _ *gock.Request) (bool, error) {
	data, err := io.ReadAll(req.Body)
	if err != nil {
		return false, err //nolint:wrapcheck // A test matcher passes the read error to gock.
	}

	req.Body = io.NopCloser(bytes.NewReader(data))

	r.mu.Lock()
	r.bodies = append(r.bodies, data)
	r.auth = append(r.auth, req.Header.Get("Authorization")+req.Header.Get("X-Api-Key"))
	r.mu.Unlock()

	return true, nil
}

// serve makes gock answer the next calls to host with the responses, in
// order, and returns the requests it receives. Each test uses its own host,
// so parallel tests never take each other's responses.
func serve(t *testing.T, host string, responses ...recorded) *requests {
	t.Helper()

	reqs := &requests{}

	for _, r := range responses {
		contentType := contentTypeSSE
		if r.status != http.StatusOK {
			contentType = contentTypeJSON
		}

		reply := gock.New("http://"+host).
			Post(messagesPath).
			AddMatcher(reqs.capture).
			Reply(r.status).
			SetHeader("Content-Type", contentType).
			File("fixture/" + r.file)

		for k, v := range r.headers {
			reply.SetHeader(k, v)
		}
	}

	return reqs
}

// newTestConnector builds a connector for modelID that sends its requests
// to host through gock. edit, when set, changes the config first.
func newTestConnector(t *testing.T, host, modelID string, edit func(*Config)) *Connector {
	t.Helper()

	ctrl := gomock.NewController(t)
	s := registry.NewMockSecrets(ctrl)
	s.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()

	client := &http.Client{}
	gock.InterceptClient(client)

	cfg := Config{
		Settings:     model.Settings{Model: modelID},
		APIKeySecret: apiKeyRef,
		BaseURL:      "http://" + host,
	}

	if edit != nil {
		edit(&cfg)
	}

	c, err := New(t.Context(), "claude", cfg, registry.Deps{Secrets: s, HTTP: client})
	require.NoError(t, err)

	conn, ok := c.(*Connector)
	require.True(t, ok)

	return conn
}

// userText is a request with one user message.
func userText(text string) model.Request {
	return model.Request{Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: text}}}}}
}

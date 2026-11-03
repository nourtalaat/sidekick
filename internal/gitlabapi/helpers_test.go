package gitlabapi

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testToken is the access token the test secret holds.
const testToken = "glpat-00000000-test-token"

// apiRoot is the path of the REST API on the fake server.
const apiRoot = "/api/v4"

// testTokenRef is the secret reference of the test token.
var testTokenRef = registry.SecretRef{Name: "sidekick-gitlab", Key: "token"}

// jsonHeaders are the headers of a JSON answer from GitLab.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// tokenSecrets returns a secrets resolver that serves the test token,
// followed by a newline as a file holds it, any number of times.
func tokenSecrets(t *testing.T) *registry.MockSecrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte(testToken+"\n"), nil).AnyTimes()

	return s
}

// newTestClient returns a client that talks to api and reads its token from
// secrets.
func newTestClient(t *testing.T, api *fakehttp.Server, secrets registry.Secrets) *Client {
	t.Helper()

	c, err := New(Options{BaseURL: api.URL(), Token: testTokenRef, Secrets: secrets, HTTP: api.Client(), UserAgent: "sidekick/test"})
	require.NoError(t, err)

	return c
}

// body returns a JSON answer with the given body.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
}

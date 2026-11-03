package argocd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	argo "github.com/nourtalaat/sidekick/internal/argocd"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// testToken is the API token the test secret holds.
const testToken = "argocd-test-token-0000"

// Paths of the fake API.
const (
	listPath = "/api/v1/applications"
)

// testTokenRef is the secret reference of the test token.
var testTokenRef = registry.SecretRef{Name: "sidekick-argocd", Key: "token"}

// testApplications are the globs of the test sources: two projects, one of
// them with a name prefix.
var testApplications = argo.Allowlist{"shop/*", "platform/ingress-*"}

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// tokenSecrets returns a secrets resolver that serves the test token,
// followed by a newline as a file holds it, any number of times.
func tokenSecrets(t *testing.T) *registry.MockSecrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte(testToken+"\n"), nil).AnyTimes()

	return s
}

// newTestSource returns a source that reads testApplications through api.
func newTestSource(t *testing.T, api *fakehttp.Server, secrets registry.Secrets) *Source {
	t.Helper()

	return newTestSourceFor(t, api, secrets, testApplications)
}

// newTestSourceFor returns a source that reads the applications the globs
// allow through api.
func newTestSourceFor(t *testing.T, api *fakehttp.Server, secrets registry.Secrets, applications argo.Allowlist) *Source {
	t.Helper()

	cfg := Config{BaseURL: api.URL(), TokenSecret: testTokenRef, Applications: applications}

	built, err := New(t.Context(), "argocd", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
	require.NoError(t, err)

	s, ok := built.(*Source)
	require.True(t, ok)

	return s
}

// body returns a JSON answer.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
}

// fixture returns a JSON answer read from a file in fixture/.
func fixture(name string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, BodyFile: "fixture/" + name}
}

// failed returns an Argo CD error answer.
func failed(status int, message string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: jsonHeaders, Body: fmt.Appendf(nil, `{"error":%q,"code":7,"message":%q}`, message, message)}
}

// withURL replaces {{URL}} in want with the base URL of the fake server.
func withURL(want, baseURL string) string {
	return strings.ReplaceAll(want, "{{URL}}", baseURL)
}

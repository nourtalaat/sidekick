package backstage

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// testToken is the token the test secret holds.
const testToken = "backstage-test-token"

// testTokenRef is the secret reference of the test token.
var testTokenRef = registry.SecretRef{Name: "sidekick-backstage", Key: "token"}

// jsonHeaders are the headers of a JSON answer from Backstage.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=utf-8"}

// tokenSecrets returns a secrets resolver that serves the test token,
// followed by a newline as a file holds it, any number of times.
func tokenSecrets(t *testing.T) *registry.MockSecrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte(testToken+"\n"), nil).AnyTimes()

	return s
}

// newTestSource returns a source that talks to api, with its web app at
// https://backstage.example.com, and asks for pageSize entities per page,
// or maxPageSize when pageSize is zero.
func newTestSource(t *testing.T, api *fakehttp.Server, secrets registry.Secrets, pageSize int) *Source {
	t.Helper()

	cfg := Config{BaseURL: api.URL(), AppURL: "https://backstage.example.com/", TokenSecret: testTokenRef}

	built, err := New(t.Context(), "backstage", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
	require.NoError(t, err)

	s, ok := built.(*Source)
	require.True(t, ok)

	if pageSize > 0 {
		s.pageSize = pageSize
	}

	return s
}

// fixture returns a JSON answer read from a file in fixture/.
func fixture(name string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, BodyFile: "fixture/" + name}
}

// failed returns a Backstage error answer with status and message.
func failed(status int, message string) fakehttp.Response {
	return fakehttp.Response{
		Status:  status,
		Headers: jsonHeaders,
		Body:    []byte(`{"error":{"name":"Error","message":"` + message + `"},"response":{"statusCode":` + strconv.Itoa(status) + `}}`),
	}
}

// entityLink returns the evidence of an entity's page.
func entityLink(r, path string) source.Evidence {
	return source.Evidence{Kind: source.EvidenceResource, Title: "Backstage " + r, URL: "https://backstage.example.com/catalog/" + path}
}

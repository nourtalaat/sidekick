package gitlab

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// testToken is the access token the test secret holds.
const testToken = "glpat-00000000-test-token"

// apiRoot is the path of the REST API on the fake server.
const apiRoot = "/api/v4"

// testTokenRef is the secret reference of the test token.
var testTokenRef = registry.SecretRef{Name: "sidekick-gitlab", Key: "token"}

// jsonHeaders are the headers of a JSON answer from GitLab.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// route is a GitLab API path and the answers it gives, in order. The path is
// the decoded path, as the fake server matches it.
type route struct {
	method    string
	path      string
	responses []fakehttp.Response
}

// tokenSecrets returns a secrets resolver that serves the test token,
// followed by a newline as a file holds it, any number of times.
func tokenSecrets(t *testing.T) *registry.MockSecrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testTokenRef).Return([]byte(testToken+"\n"), nil).AnyTimes()

	return s
}

// newTestSource returns a source that talks to api, and asks for pageSize
// items per page, or the most GitLab sends when pageSize is zero.
func newTestSource(t *testing.T, api *fakehttp.Server, pageSize int) *Source {
	t.Helper()

	return newTestSourceWith(t, api, tokenSecrets(t), pageSize)
}

// newTestSourceWith is newTestSource with the given secrets resolver.
func newTestSourceWith(t *testing.T, api *fakehttp.Server, secrets registry.Secrets, pageSize int) *Source {
	t.Helper()

	cfg := Config{
		BaseURL:         api.URL(),
		TokenSecret:     testTokenRef,
		Projects:        []string{"acme/**"},
		ServiceProjects: map[string]string{"checkout-api": "acme/checkout"},
	}

	built, err := New(t.Context(), "gitlab", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
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

// page returns a JSON answer read from a file in fixture/ that says page
// next comes after it with an x-next-page header, or says nothing when next
// is zero.
func page(name string, next int) fakehttp.Response {
	resp := fixture(name)
	if next > 0 {
		resp.Headers = map[string]string{
			"Content-Type": jsonHeaders["Content-Type"],
			"X-Next-Page":  fmt.Sprint(next),
		}
	}

	return resp
}

// body returns a JSON answer with the given body.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
}

// failed returns a GitLab error answer.
func failed(status int, message string) fakehttp.Response {
	return fakehttp.Response{Status: status, Headers: jsonHeaders, Body: fmt.Appendf(nil, `{"message":%q}`, message)}
}

// queries returns the encoded query of every request api received for one
// of paths, in order.
func queries(api *fakehttp.Server, paths []string) []string {
	var out []string

	for _, r := range api.Requests() {
		if slices.Contains(paths, r.Path) {
			out = append(out, r.Query.Encode())
		}
	}

	return out
}

// commitLink returns the evidence of a commit of acme/checkout.
func commitLink(short, sha string, when time.Time) source.Evidence {
	return source.Evidence{
		Kind:  source.EvidenceCommit,
		Title: "acme/checkout@" + short,
		URL:   "https://gitlab.example.com/acme/checkout/-/commit/" + sha,
		At:    when,
	}
}

// at returns a time on 2026-05-04, the day of the test data, in UTC.
func at(hour, minute int) time.Time {
	return time.Date(2026, 5, 4, hour, minute, 0, 0, time.UTC)
}

// commitsPath is the path of the commit list of acme/checkout.
const commitsPath = apiRoot + "/projects/acme/checkout/repository/commits"

package launchdarkly

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// testToken is the access token the test secret holds.
const testToken = "api-00000000-test-token"

// appURL is the base URL of the test sources, the EU instance, which the
// links in results start with. The requests go to the fake server.
const appURL = "https://app.eu.launchdarkly.com"

// testTokenRef is the secret reference of the test token.
var testTokenRef = registry.SecretRef{Name: "sidekick-launchdarkly", Key: "token"}

// testNow is the fixed time of the test clocks.
var testNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// testProjects is the allowlist of the test sources: one project with two
// environments, so calls must name the environment.
var testProjects = flags.Allowlist{{Key: "shop", Environments: []string{"production", "staging"}}}

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

// newTestSource returns a source that reads testProjects through api, with
// links to appURL and a fixed clock.
func newTestSource(t *testing.T, api *fakehttp.Server, secrets registry.Secrets) *Source {
	t.Helper()

	cfg := Config{TokenSecret: testTokenRef, Projects: testProjects, BaseURL: appURL + "/"}

	built, err := New(t.Context(), "launchdarkly", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
	require.NoError(t, err)

	s, ok := built.(*Source)
	require.True(t, ok)

	s.client.baseURL = api.URL()
	s.now = func() time.Time { return testNow }

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

// failed returns a LaunchDarkly error answer.
func failed(status int, code, message string) fakehttp.Response {
	return fakehttp.Response{
		Status:  status,
		Headers: jsonHeaders,
		Body:    fmt.Appendf(nil, `{"code":%q,"message":%q,"id":"req-1"}`, code, message),
	}
}

// millis returns the Unix time in milliseconds of a time on the test day.
func millis(hour, minute int) int64 {
	return time.Date(2026, 5, 4, hour, minute, 0, 0, time.UTC).UnixMilli()
}

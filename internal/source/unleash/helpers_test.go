package unleash

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source/flags"
)

// testToken is the service account token the test secret holds.
const testToken = "*:*.0000000000000000000000000000000000000000000000000000"

// appURL is the base URL of the test sources, which the links in results
// start with. The requests go to the fake server.
const appURL = "https://unleash.example.com"

// testTokenRef is the secret reference of the test token.
var testTokenRef = registry.SecretRef{Name: "sidekick-unleash", Key: "token"}

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

	cfg := Config{BaseURL: appURL + "/", TokenSecret: testTokenRef, Projects: testProjects}

	built, err := New(t.Context(), "unleash", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
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

// failed returns an Unleash error answer, shaped as current versions send
// them.
func failed(status int, name, message string) fakehttp.Response {
	return fakehttp.Response{
		Status:  status,
		Headers: jsonHeaders,
		Body: fmt.Appendf(nil, `{"id":"req-1","name":%q,"message":%q,"details":[{"message":%q,"description":%q}]}`,
			name, message, message, message),
	}
}

// at returns a time on the test day.
func at(hour, minute, second int) time.Time {
	return time.Date(2026, 5, 4, hour, minute, second, 0, time.UTC)
}

// eventsPage returns an event search answer with total events in all and
// the events at the given times, newest first. Each event is a flag
// change in shop/production; its ID is its time in seconds, so a time
// that is given twice is the same event.
func eventsPage(total int, times ...time.Time) string {
	items := make([]string, 0, len(times))

	for _, ts := range times {
		items = append(items, fmt.Sprintf(`{"id":%d,"createdAt":%q,"type":"feature-environment-enabled","createdBy":"ada@example.com",
			"environment":"production","project":"shop","featureName":"flag-%d","summary":"**ada@example.com** enabled **flag-%d**"}`,
			ts.Unix(), ts.Format(time.RFC3339), ts.Unix(), ts.Unix()))
	}

	return fmt.Sprintf(`{"events":[%s],"total":%d}`, strings.Join(items, ","), total)
}

// every returns n times, the first at first and each one step earlier than
// the one before it.
func every(first time.Time, step time.Duration, n int) []time.Time {
	out := make([]time.Time, 0, n)
	for i := range n {
		out = append(out, first.Add(-time.Duration(i)*step))
	}

	return out
}

package gcp

import (
	"fmt"
	"testing"
	"time"

	"golang.org/x/oauth2"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/source"
)

// testToken is the access token the test sources send.
const testToken = "test-access-token"

// testProject is the project the test sources read.
const testProject = "shop-prod"

// testNow is the fixed time of the test clocks.
var testNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=UTF-8"}

// route is an API path and the answers it gives, in order.
type route struct {
	method    string
	path      string
	responses []fakehttp.Response
}

// failingTokens is a token source that always fails.
type failingTokens struct {
	err error
}

// Token returns the error.
func (f failingTokens) Token() (*oauth2.Token, error) {
	return nil, f.err
}

// newTestSource returns a source that reads projects, or testProject, with
// every API on api and a fixed clock.
func newTestSource(t *testing.T, api *fakehttp.Server, projects ...string) *Source {
	t.Helper()

	if len(projects) == 0 {
		projects = []string{testProject}
	}

	return &Source{
		cfg: Config{Projects: projects},
		client: &client{
			http:      api.Client(),
			tokens:    oauth2.StaticTokenSource(&oauth2.Token{AccessToken: testToken}),
			userAgent: "sidekick/test",
		},
		apis: endpoints{
			logging:       api.URL() + "/logging/v2",
			monitoring:    api.URL() + "/monitoring/v3",
			prometheus:    api.URL() + "/prometheus/v1",
			container:     api.URL() + "/container/v1",
			serviceHealth: api.URL() + "/servicehealth/v1",
		},
		now: func() time.Time { return testNow },
	}
}

// body returns a JSON answer.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
}

// failed returns a Google Cloud error answer.
func failed(code int, status, message string) fakehttp.Response {
	return fakehttp.Response{
		Status:  code,
		Headers: jsonHeaders,
		Body:    fmt.Appendf(nil, `{"error":{"code":%d,"message":%q,"status":%q}}`, code, message, status),
	}
}

// at returns a time on the test day.
func at(hour, minute int) time.Time {
	return time.Date(2026, 5, 4, hour, minute, 0, 0, time.UTC)
}

// logsLink returns the evidence of a Logs Explorer query.
func logsLink(kind source.EvidenceKind, title, filter string, w window) source.Evidence {
	return source.Evidence{Kind: kind, Title: title, URL: logsExplorer(testProject, filter, w), At: w.end}
}

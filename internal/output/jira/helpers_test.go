package jira

import (
	"net/http"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/event"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// The fake Jira site and what it knows.
const (
	testEmail     = "sidekick@example.com"
	testToken     = "jira-token"
	testIssueID   = "10042"
	testCommentID = "10100"
	testOutput    = "jira-comment"
	testInvest    = "0199c9d2-7f00-7000-8000-000000000001"
)

// The API paths of the fake Jira site.
const (
	pathIssue    = "/rest/api/3/issue/OPS-123"
	pathComments = "/rest/api/3/issue/" + testIssueID + "/comment"
	pathComment  = pathComments + "/" + testCommentID
)

// tokenRef is the secret reference of the API token in the tests.
var tokenRef = registry.SecretRef{Name: "sidekick-jira", Key: "apiToken"}

// testConfig is a valid config for the fake site at api.
func testConfig(api *fakehttp.Server) Config {
	return Config{URL: api.URL(), Email: testEmail, APITokenSecret: tokenRef, Projects: []string{"OPS"}}
}

// testSink builds a Jira output against api, with an API token whose file
// ends in a newline.
func testSink(t *testing.T, api *fakehttp.Server) output.Sink {
	t.Helper()

	sec := registry.NewMockSecrets(gomock.NewController(t))
	sec.EXPECT().Resolve(gomock.Any(), tokenRef).Return([]byte(testToken+"\n"), nil).AnyTimes()

	sink, err := New(t.Context(), testOutput, testConfig(api), registry.Deps{Secrets: sec, HTTP: api.Client(), Metrics: prometheus.NewRegistry()})
	require.NoError(t, err)

	return sink
}

// requireSignedIn checks that every request api received has the API
// token's basic auth.
func requireSignedIn(t *testing.T, api *fakehttp.Server) {
	t.Helper()

	for _, r := range api.Requests() {
		user, pass, ok := (&http.Request{Header: r.Header}).BasicAuth()
		require.True(t, ok)
		require.Equal(t, testEmail, user)
		require.Equal(t, testToken, pass)
	}
}

// finalUpdate is a final update for an event that names OPS-123 in a
// label.
func finalUpdate() output.Update {
	return output.Update{
		InvestigationID: testInvest,
		Phase:           output.PhaseFinal,
		Seq:             5,
		Event: event.Event{
			Title:  "checkout-api p99 latency above 2s",
			Labels: map[string]string{defaultLabel: "OPS-123"},
		},
		Report: &report.Report{
			Verdict:    report.TruePositive,
			Confidence: report.High,
			Reason:     "A deploy at 10:02 doubled the database calls per checkout.",
			Cause:      "Deploy 4f2a of checkout-api removed a query cache.",
			Impact:     "Checkout p99 is 2.8s for about 30% of requests.",
			Findings: []report.Finding{
				{Claim: "Database calls per request went from 4 to 9 at 10:02.", Evidence: []source.Evidence{{Kind: "query", Title: "DB calls per request", URL: "https://signoz.example.com/q/1"}}},
				{Claim: "The cache was removed on purpose."},
			},
			Fixes:      []report.Fix{{Title: "Roll back deploy 4f2a", Detail: "Restores the cache within minutes."}},
			Checked:    []report.Check{{Source: "signoz", Status: report.CheckOK}},
			StopReason: report.StopSubmitted,
		},
	}
}

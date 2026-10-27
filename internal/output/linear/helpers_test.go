package linear

import (
	"encoding/json"
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

// The fake Linear API and what it knows.
const (
	pathGraphQL   = "/graphql"
	testAPIKey    = "lin_api_test"
	testIssueID   = "7a1f3a52-6c1e-4bb0-9f4e-0c6b1d2a3e41"
	testCommentID = "0b8f1e7c-2d4a-4f3b-8c1d-5e6f7a8b9c0d"
	testOutput    = "linear-comment"
	testInvest    = "0199c9d2-7f00-7000-8000-000000000001"
)

// apiKeyRef is the secret reference of the API key in the tests.
var apiKeyRef = registry.SecretRef{Name: "sidekick-linear", Key: "apiKey"}

// sent is a GraphQL request as the fake server received it.
type sent struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// testConfig is a valid config for the fake server at api.
func testConfig(api *fakehttp.Server) Config {
	return Config{
		APIKeySecret: apiKeyRef,
		Workspace:    "acme",
		Teams:        []string{"OPS"},
		APIURL:       api.URL() + pathGraphQL,
	}
}

// testSink builds a Linear output against api, with an API key whose file
// ends in a newline.
func testSink(t *testing.T, api *fakehttp.Server) output.Sink {
	t.Helper()

	sink, err := New(t.Context(), testOutput, testConfig(api), testDeps(t, api))
	require.NoError(t, err)

	return sink
}

// testDeps returns deps with the API key and the fake server's client.
func testDeps(t *testing.T, api *fakehttp.Server) registry.Deps {
	t.Helper()

	sec := registry.NewMockSecrets(gomock.NewController(t))
	sec.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()

	return registry.Deps{Secrets: sec, HTTP: api.Client(), Metrics: prometheus.NewRegistry()}
}

// requests decodes the GraphQL requests api received.
func requests(t *testing.T, api *fakehttp.Server) []sent {
	t.Helper()

	var out []sent

	for _, r := range api.Requests() {
		require.Equal(t, testAPIKey, r.Header.Get("Authorization"))

		var s sent
		require.NoError(t, json.Unmarshal(r.Body, &s))

		out = append(out, s)
	}

	return out
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
			Checked:    []report.Check{{Source: "signoz", Status: report.CheckOK}, {Source: "github", Status: report.CheckOK}},
			StopReason: report.StopSubmitted,
		},
	}
}

// untrustedUpdate is a final update whose event and report text try to
// add links, images, mentions, HTML and formatting.
func untrustedUpdate() output.Update {
	u := finalUpdate()
	u.Event.Title = "**urgent** <img src=x onerror=alert(1)> @channel"
	u.Report.Reason = "See [the fix](https://evil.example.com) and ![x](https://evil.example.com/p.png)\n# Heading\n1. not a list\n> quote | table | ~~gone~~ :tada: &amp; `code` \\"
	u.Report.Findings = []report.Finding{{Claim: "line one\n- injected item", Evidence: []source.Evidence{{Title: "](javascript:alert(1)) [x", URL: "https://signoz.example.com/q/(1)"}}}}
	u.Report.Fixes = []report.Fix{{Title: "_italic_ fix", Detail: "* also\n2. this"}}

	return u
}

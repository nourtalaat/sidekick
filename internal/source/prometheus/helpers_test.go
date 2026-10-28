package prometheus

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// testWebURL is the Prometheus web UI the test sources link to.
const testWebURL = "https://prometheus.example.com"

// testNow is the fixed time of the test clocks.
var testNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// lastHour is the default window of the tools at testNow.
var lastHour = window{start: testNow.Add(-time.Hour), end: testNow}

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json"}

// route is an API path and the answers it gives, in order.
type route struct {
	method    string
	path      string
	responses []fakehttp.Response
}

// newTestSource returns a source built by New from cfg, with its base URL
// on api and its links to testWebURL unless cfg sets them, and a fixed
// clock.
func newTestSource(t *testing.T, api *fakehttp.Server, cfg Config, secrets registry.Secrets) *Source {
	t.Helper()

	if cfg.BaseURL == "" {
		cfg.BaseURL = api.URL()
	}

	if cfg.Links == (Links{}) {
		cfg.Links.PrometheusURL = testWebURL
	}

	require.NoError(t, cfg.Validate())

	built, err := New(t.Context(), "prometheus", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
	require.NoError(t, err)

	s, ok := built.(*Source)
	require.True(t, ok)

	s.now = func() time.Time { return testNow }

	return s
}

// secretsOf returns a secrets resolver that serves values by secret name,
// any number of times.
func secretsOf(t *testing.T, values map[string]string) *registry.MockSecrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	for name, value := range values {
		s.EXPECT().Resolve(gomock.Any(), registry.SecretRef{Name: name, Key: "value"}).Return([]byte(value), nil).AnyTimes()
	}

	return s
}

// ref returns the reference of a test secret.
func ref(name string) *registry.SecretRef {
	return &registry.SecretRef{Name: name, Key: "value"}
}

// body returns a JSON answer.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
}

// success returns a Prometheus API answer with data.
func success(data string) fakehttp.Response {
	return body(`{"status":"success","data":` + data + `}`)
}

// failed returns a Prometheus API error answer.
func failed(code int, errorType, message string) fakehttp.Response {
	return fakehttp.Response{
		Status:  code,
		Headers: jsonHeaders,
		Body:    fmt.Appendf(nil, `{"status":"error","errorType":%q,"error":%q}`, errorType, message),
	}
}

// graphLink returns the evidence of a range query in the test web UI.
func graphLink(title, expr, rangeInput string, end time.Time) source.Evidence {
	params := url.Values{"g0.expr": {expr}, "g0.tab": {"0"}, "g0.range_input": {rangeInput}, "g0.end_input": {end.Format(promUITime)}}

	return source.Evidence{Kind: source.EvidenceMetric, Title: title + " in Prometheus", URL: testWebURL + "/graph?" + params.Encode(), At: end}
}

// tableLink returns the evidence of an instant query in the test web UI.
func tableLink(title, expr string, at time.Time) source.Evidence {
	params := url.Values{"g0.expr": {expr}, "g0.tab": {"1"}, "g0.moment_input": {at.Format(promUITime)}}

	return source.Evidence{Kind: source.EvidenceMetric, Title: title + " in Prometheus", URL: testWebURL + "/graph?" + params.Encode(), At: at}
}

// callCase is one tool call and what it should give.
type callCase struct {
	name    string
	cfg     Config
	secrets map[string]string
	tool    string
	input   string
	routes  []route
	// wantQueries are the encoded queries of the requests, in order.
	wantQueries []string
	want        string
	// wantEvidence is the evidence of the result, where {api} in a URL
	// stands for the fake server's URL.
	wantEvidence  []source.Evidence
	wantTruncated bool
	wantNoData    bool
	wantText      string
	wantToolError string
	wantErr       string
}

// runCalls runs each case against a fake server of its own.
func runCalls(t *testing.T, tests []callCase) {
	t.Helper()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			api := fakehttp.New(t)
			for _, r := range tt.routes {
				api.Handle(r.method, r.path, r.responses...)
			}

			api.ExpectAllCalled()

			cfg := tt.cfg
			if cfg.AlertmanagerURL == "alertmanager" {
				cfg.AlertmanagerURL = api.URL() + "/alertmanager"
			}

			s := newTestSource(t, api, cfg, secretsOf(t, tt.secrets))

			res, err := s.Call(t.Context(), source.Call{Tool: tt.tool, Input: json.RawMessage(tt.input)})

			if tt.wantQueries != nil {
				var got []string
				for _, r := range api.Requests() {
					got = append(got, r.Query.Encode())
				}

				require.Equal(t, tt.wantQueries, got)
			}

			wantEvidence := withAPI(tt.wantEvidence, api.URL())

			switch {
			case tt.wantErr != "":
				require.ErrorContains(t, err, tt.wantErr)

				_, isToolErr := errors.AsType[*source.ToolError](err)
				require.False(t, isToolErr, "want a failure of the source, got a tool error")
			case tt.wantToolError != "":
				toolErr, ok := errors.AsType[*source.ToolError](err)
				require.True(t, ok, "want a tool error, got %v", err)
				require.Contains(t, toolErr.Message, tt.wantToolError)
			case tt.wantNoData:
				require.NoError(t, err)
				require.True(t, res.NoData)
				require.Equal(t, tt.wantText, res.Text)
				require.Equal(t, wantEvidence, res.Evidence)
				require.Empty(t, res.Structured)
			default:
				require.NoError(t, err)
				require.JSONEq(t, tt.want, string(res.Structured))
				require.Equal(t, wantEvidence, res.Evidence)
				require.Equal(t, tt.wantTruncated, res.Truncated)
				require.False(t, res.NoData)
			}
		})
	}
}

// withAPI returns evidence with {api} in each URL replaced by apiURL.
func withAPI(evidence []source.Evidence, apiURL string) []source.Evidence {
	if evidence == nil {
		return nil
	}

	out := slices.Clone(evidence)
	for i := range out {
		out[i].URL = strings.ReplaceAll(out[i].URL, "{api}", apiURL)
	}

	return out
}

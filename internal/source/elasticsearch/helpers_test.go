package elasticsearch

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
)

// Credentials of the test sources.
const (
	testAPIKey   = "test-api-key"
	testUsername = "sidekick"
	testPassword = "test-password"
)

// testDiscoverURL is the discoverURL template of the test sources.
const testDiscoverURL = "https://kibana.example.com/app/discover#/?_g=(time:(from:'{{.From}}',to:'{{.To}}'))" +
	"&_a=(index:'{{.Index}}',query:(language:lucene,query:'{{.Query}}'))"

// Secret references of the test sources.
var (
	apiKeyRef   = registry.SecretRef{Name: "sidekick-elasticsearch", Key: "apiKey"}
	passwordRef = registry.SecretRef{Name: "sidekick-opensearch", Key: "password"}
)

// testNow is the fixed time of the test clocks.
var testNow = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// flavors are the flavors every call test runs with.
var flavors = []string{FlavorElasticsearch, FlavorOpenSearch}

// jsonHeaders are the headers of a JSON answer.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=UTF-8"}

// route is an API path and the answers it gives, in order.
type route struct {
	method    string
	path      string
	responses []fakehttp.Response
}

// testConfig returns the config of a test source of flavor on api, with
// patterns as the allowlist, or logs-* without any. Elasticsearch signs in
// with an API key and OpenSearch with a password.
func testConfig(api *fakehttp.Server, flavor string, patterns ...string) Config {
	if len(patterns) == 0 {
		patterns = []string{"logs-*"}
	}

	cfg := Config{Flavor: flavor, BaseURL: api.URL(), IndexPatterns: patterns, DiscoverURL: testDiscoverURL}

	if flavor == FlavorOpenSearch {
		cfg.BasicAuth = &BasicAuth{Username: testUsername, PasswordSecret: passwordRef}
	} else {
		cfg.APIKeySecret = &apiKeyRef
	}

	return cfg
}

// newTestSource returns a source built by New from cfg, with secrets that
// hold the test credentials and a fixed clock.
func newTestSource(t *testing.T, api *fakehttp.Server, cfg Config) *Source {
	t.Helper()

	secrets := registry.NewMockSecrets(gomock.NewController(t))
	secrets.EXPECT().Resolve(gomock.Any(), apiKeyRef).Return([]byte(testAPIKey+"\n"), nil).AnyTimes()
	secrets.EXPECT().Resolve(gomock.Any(), passwordRef).Return([]byte(testPassword), nil).AnyTimes()

	src, err := New(t.Context(), "logs", cfg, registry.Deps{HTTP: api.Client(), Secrets: secrets})
	require.NoError(t, err)

	s, ok := src.(*Source)
	require.True(t, ok)

	s.now = func() time.Time { return testNow }

	return s
}

// wantAuthorization returns the Authorization header a test source of
// flavor sends.
func wantAuthorization(flavor string) string {
	if flavor == FlavorOpenSearch {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(testUsername+":"+testPassword))
	}

	return "ApiKey " + testAPIKey
}

// product returns the product name of flavor in messages.
func product(flavor string) string {
	if flavor == FlavorOpenSearch {
		return "OpenSearch"
	}

	return "Elasticsearch"
}

// body returns a JSON answer.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
}

// failed returns an error answer in the shape both flavors send.
func failed(code int, kind, reason string) fakehttp.Response {
	return fakehttp.Response{
		Status:  code,
		Headers: jsonHeaders,
		Body: fmt.Appendf(nil, `{"error":{"root_cause":[{"type":%q,"reason":%q}],"type":%q,"reason":%q},"status":%d}`,
			kind, reason, kind, reason, code),
	}
}

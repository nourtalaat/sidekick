package github

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Test values of the GitHub App.
const (
	testAppID          = 123456
	testInstallationID = 7890123
	testKeyBits        = 2048
)

// testExchangePath is the token exchange route of the test installation.
const testExchangePath = "/app/installations/7890123/access_tokens"

// testKeyRef is the secret reference of the test private key.
var testKeyRef = registry.SecretRef{Name: "sidekick-github", Key: "private-key.pem"}

// testStart is the fixed time the test clocks start at.
var testStart = time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC)

// testKey returns the RSA key the tests sign with, made once per test binary
// because making one is slow.
var testKey = sync.OnceValues(func() (*rsa.PrivateKey, error) {
	return rsa.GenerateKey(rand.Reader, testKeyBits)
})

// clock is a test clock that only moves when the test moves it.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

// Now returns the clock's time.
func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Set moves the clock to t.
func (c *clock) Set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = t
}

// keyPEM returns the test key as a PKCS #1 PEM block, the form GitHub
// gives app private keys in.
func keyPEM(t *testing.T) []byte {
	t.Helper()

	key, err := testKey()
	require.NoError(t, err)

	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
}

// keySecrets returns a secrets resolver that serves the test key times
// times.
func keySecrets(t *testing.T, times int) *registry.MockSecrets {
	t.Helper()

	s := registry.NewMockSecrets(gomock.NewController(t))
	s.EXPECT().Resolve(gomock.Any(), testKeyRef).Return(keyPEM(t), nil).Times(times)

	return s
}

// tokenResponse returns the answer of a token exchange.
func tokenResponse(token string, expires time.Time) fakehttp.Response {
	return fakehttp.Response{
		Status:  http.StatusCreated,
		Headers: map[string]string{"Content-Type": "application/json"},
		Body:    fmt.Appendf(nil, `{"token":%q,"expires_at":%q}`, token, expires.Format(time.RFC3339)),
	}
}

// newTestTokens returns installation tokens that exchange with api.
func newTestTokens(api *fakehttp.Server, secrets registry.Secrets, now func() time.Time) *installationTokens {
	return &installationTokens{
		appID:          testAppID,
		installationID: testInstallationID,
		privateKey:     testKeyRef,
		secrets:        secrets,
		client:         api.Client(),
		baseURL:        api.URL() + "/",
		userAgent:      "sidekick-test",
		now:            now,
	}
}

// jsonHeaders are the headers of a JSON answer from GitHub.
var jsonHeaders = map[string]string{"Content-Type": "application/json; charset=utf-8"}

// route is a GitHub API path and the answers it gives, in order.
type route struct {
	method    string
	path      string
	responses []fakehttp.Response
}

// newTestSource returns a source that talks to api, reads the private key
// exchanges times, and asks for pageSize items per page, or the most
// GitHub sends when pageSize is zero.
func newTestSource(t *testing.T, api *fakehttp.Server, exchanges, pageSize int) *Source {
	t.Helper()

	cfg := Config{
		BaseURL:      api.URL(),
		App:          App{AppID: testAppID, InstallationID: testInstallationID, PrivateKeySecret: testKeyRef},
		Repos:        []string{"acme/*"},
		ServiceRepos: map[string]string{"checkout-api": "acme/checkout"},
	}

	built, err := New(t.Context(), "github", cfg, registry.Deps{HTTP: api.Client(), Secrets: keySecrets(t, exchanges)})
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

// page returns a JSON answer read from a file in fixture/ that links to
// page next, or to no next page when next is zero.
func page(name string, next int) fakehttp.Response {
	resp := fixture(name)
	if next > 0 {
		resp.Headers = map[string]string{
			"Content-Type": jsonHeaders["Content-Type"],
			"Link":         fmt.Sprintf(`<https://api.github.com/resource?page=%d>; rel="next"`, next),
		}
	}

	return resp
}

// body returns a JSON answer with the given body.
func body(s string) fakehttp.Response {
	return fakehttp.Response{Headers: jsonHeaders, Body: []byte(s)}
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
		URL:   "https://github.com/acme/checkout/commit/" + sha,
		At:    when,
	}
}

// at returns a time on 2026-05-04, the day of the test data, in UTC.
func at(hour, minute int) time.Time {
	return time.Date(2026, 5, 4, hour, minute, 0, 0, time.UTC)
}

//go:build integration

package main

import (
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/auth/authtest"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// uiBuiltEnv is set by `make test-integration` after it builds the UI. The
// test then requires the real UI; without it, the test accepts the page
// for builds without the UI and checks only what that page shares with it.
const uiBuiltEnv = "SIDEKICK_UI_BUILT"

const uiContentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// rootElement is where the app mounts, in index.html.
const rootElement = `<div id="root"></div>`

var scriptSrc = regexp.MustCompile(`src="(/assets/[^"]+\.js)"`)

// TestServeWebUI runs `sidekick serve` with an oidc section and checks that
// UI paths return index.html with the security headers, that hashed files
// are cached for good, and that the paths of the API, the login and the
// webhooks are not answered by the UI. Without an oidc section, the UI
// paths answer with the page that says the UI needs one.
func TestServeWebUI(t *testing.T) {
	t.Parallel()

	t.Run("with oidc", func(t *testing.T) {
		t.Parallel()

		provider := authtest.NewProvider(t, "sidekick")
		content := oidcServeConfig(fakeAnthropic(t).URL(), provider.Issuer())
		cmd, lines, started := startServe(t, writeConfig(t, content, storetest.NewURL(t), "oidc/clientSecret"))
		base := "http://" + started.Listen

		uiRequired := os.Getenv(uiBuiltEnv) != ""

		for _, urlPath := range []string{"/", "/investigations/42", "/settings"} {
			resp, body := fetch(t, base+urlPath)
			require.Equal(t, http.StatusOK, resp.StatusCode, urlPath)
			requireUIHeaders(t, resp)
			require.Equal(t, "no-cache", resp.Header.Get("Cache-Control"), urlPath)
			require.Contains(t, resp.Header.Get("Content-Type"), "text/html")

			hasUI := strings.Contains(body, rootElement)
			require.True(t, hasUI || !uiRequired, "the UI is built into the binary")

			if urlPath != "/" || !hasUI {
				continue
			}

			match := scriptSrc.FindStringSubmatch(body)
			require.NotNil(t, match, "index.html loads a hashed script")

			asset, _ := fetch(t, base+match[1])
			require.Equal(t, http.StatusOK, asset.StatusCode)
			require.Equal(t, "public, max-age=31536000, immutable", asset.Header.Get("Cache-Control"))
			require.Contains(t, asset.Header.Get("Content-Type"), "javascript")
			requireUIHeaders(t, asset)
		}

		missing, _ := fetch(t, base+"/assets/index-no-such-file.js")
		require.Equal(t, http.StatusNotFound, missing.StatusCode, "a file of an earlier build is not answered with index.html")

		resp, body := fetch(t, base+"/api/v1/me")
		require.Equal(t, http.StatusUnauthorized, resp.StatusCode, "the API answers, not the UI")
		require.Contains(t, body, `"status":401`)

		for _, urlPath := range []string{"/api/v1/nothing", "/api/nothing", "/auth/nothing", "/hooks/nothing", "/hooks/", "/slack/events", "/teams/messages", "/healthz", "/readyz"} {
			resp, body := fetch(t, base+urlPath)
			require.NotContains(t, body, rootElement, urlPath)
			require.Empty(t, resp.Header.Get("Content-Security-Policy"), urlPath)
		}

		stopServe(t, cmd, lines, syscall.SIGTERM)
	})

	t.Run("without oidc", func(t *testing.T) {
		t.Parallel()

		cmd, lines, started := startServe(t, writeConfig(t, serveConfig(fakeAnthropic(t).URL()), storetest.NewURL(t)))

		resp, body := fetch(t, "http://"+started.Listen+"/investigations")
		require.Equal(t, http.StatusOK, resp.StatusCode)
		requireUIHeaders(t, resp)
		require.Contains(t, body, "needs an oidc section")
		require.Contains(t, body, "docs/web-ui.md")

		stopServe(t, cmd, lines, syscall.SIGTERM)
	})
}

func requireUIHeaders(t *testing.T, resp *http.Response) {
	t.Helper()

	require.Equal(t, uiContentSecurityPolicy, resp.Header.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", resp.Header.Get("X-Content-Type-Options"))
	require.Equal(t, "same-origin", resp.Header.Get("Referrer-Policy"))
}

// fetch gets url and returns the response, whose body is already read, and
// the body.
func fetch(t *testing.T, url string) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp, string(body)
}

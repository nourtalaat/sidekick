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

// fetched is an HTTP answer that has been read to the end.
type fetched struct {
	status int
	header http.Header
	body   string
}

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
			page := fetch(t, base+urlPath)
			require.Equal(t, http.StatusOK, page.status, urlPath)
			requireUIHeaders(t, page)
			require.Equal(t, "no-cache", page.header.Get("Cache-Control"), urlPath)
			require.Contains(t, page.header.Get("Content-Type"), "text/html")

			hasUI := strings.Contains(page.body, rootElement)
			require.True(t, hasUI || !uiRequired, "the UI is built into the binary")

			if urlPath != "/" || !hasUI {
				continue
			}

			match := scriptSrc.FindStringSubmatch(page.body)
			require.NotNil(t, match, "index.html loads a hashed script")

			asset := fetch(t, base+match[1])
			require.Equal(t, http.StatusOK, asset.status)
			require.Equal(t, "public, max-age=31536000, immutable", asset.header.Get("Cache-Control"))
			require.Contains(t, asset.header.Get("Content-Type"), "javascript")
			requireUIHeaders(t, asset)
		}

		missing := fetch(t, base+"/assets/index-no-such-file.js")
		require.Equal(t, http.StatusNotFound, missing.status, "a file of an earlier build is not answered with index.html")

		me := fetch(t, base+"/api/v1/me")
		require.Equal(t, http.StatusUnauthorized, me.status, "the API answers, not the UI")
		require.Contains(t, me.body, `"status":401`)

		for _, urlPath := range []string{"/api/v1/nothing", "/api/nothing", "/auth/nothing", "/hooks/nothing", "/hooks/", "/slack/events", "/teams/messages", "/healthz", "/readyz"} {
			other := fetch(t, base+urlPath)
			require.NotContains(t, other.body, rootElement, urlPath)
			require.Empty(t, other.header.Get("Content-Security-Policy"), urlPath)
		}

		stopServe(t, cmd, lines, syscall.SIGTERM)
	})

	t.Run("without oidc", func(t *testing.T) {
		t.Parallel()

		cmd, lines, started := startServe(t, writeConfig(t, serveConfig(fakeAnthropic(t).URL()), storetest.NewURL(t)))

		page := fetch(t, "http://"+started.Listen+"/investigations")
		require.Equal(t, http.StatusOK, page.status)
		requireUIHeaders(t, page)
		require.Contains(t, page.body, "needs an oidc section")
		require.Contains(t, page.body, "docs/web-ui.md")

		stopServe(t, cmd, lines, syscall.SIGTERM)
	})
}

func requireUIHeaders(t *testing.T, page fetched) {
	t.Helper()

	require.Equal(t, uiContentSecurityPolicy, page.header.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", page.header.Get("X-Content-Type-Options"))
	require.Equal(t, "same-origin", page.header.Get("Referrer-Policy"))
}

// fetch gets url and reads the whole answer.
func fetch(t *testing.T, url string) fetched {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err)

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return fetched{status: resp.StatusCode, header: resp.Header, body: string(body)}
}

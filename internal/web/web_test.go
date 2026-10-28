package web

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/stretchr/testify/require"
)

const (
	indexHTML  = `<!doctype html><div id="root"></div>`
	assetPath  = "/assets/index-3f9a1c.js"
	fontPath   = "/assets/inter-latin-400-normal-8d2b.woff2"
	faviconSVG = "<svg></svg>"
)

func testSite() fstest.MapFS {
	return fstest.MapFS{
		"index.html":             {Data: []byte(indexHTML)},
		"favicon.svg":            {Data: []byte(faviconSVG)},
		"assets/index-3f9a1c.js": {Data: []byte("console.log(1)")},
		"assets/inter-latin-400-normal-8d2b.woff2": {Data: []byte("font")},
	}
}

func TestHandler(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		enabled    bool
		method     string
		path       string
		wantStatus int
		wantBody   string
		wantCache  string
		wantType   string
	}{
		{name: "the root is index.html", enabled: true, path: "/", wantStatus: http.StatusOK, wantBody: indexHTML, wantCache: "no-cache", wantType: "text/html; charset=utf-8"},
		{name: "an app route is index.html", enabled: true, path: "/investigations/42", wantStatus: http.StatusOK, wantBody: indexHTML, wantCache: "no-cache"},
		{name: "a path with dots is cleaned", enabled: true, path: "/a/../b", wantStatus: http.StatusOK, wantBody: indexHTML, wantCache: "no-cache"},
		{name: "a hashed script is immutable", enabled: true, path: assetPath, wantStatus: http.StatusOK, wantBody: "console.log(1)", wantCache: "public, max-age=31536000, immutable", wantType: "text/javascript; charset=utf-8"},
		{name: "a font has its own type", enabled: true, path: fontPath, wantStatus: http.StatusOK, wantBody: "font", wantCache: "public, max-age=31536000, immutable", wantType: "font/woff2"},
		{name: "a missing asset is 404, not index.html", enabled: true, path: "/assets/index-old.js", wantStatus: http.StatusNotFound},
		{name: "a file next to index.html is served", enabled: true, path: "/favicon.svg", wantStatus: http.StatusOK, wantBody: faviconSVG, wantCache: "no-cache"},
		{name: "HEAD has no body", enabled: true, method: http.MethodHead, path: "/", wantStatus: http.StatusOK, wantCache: "no-cache"},
		{name: "POST is not allowed", enabled: true, method: http.MethodPost, path: "/", wantStatus: http.StatusMethodNotAllowed},
		{name: "without oidc every path says it needs oidc", enabled: false, path: "/investigations", wantStatus: http.StatusOK, wantBody: "needs an oidc section", wantCache: "no-cache"},
		{name: "without oidc assets are not served", enabled: false, path: assetPath, wantStatus: http.StatusOK, wantBody: "needs an oidc section", wantCache: "no-cache"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, err := newHandler(testSite(), tt.enabled)
			require.NoError(t, err)

			method := tt.method
			if method == "" {
				method = http.MethodGet
			}

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, tt.path, nil))

			require.Equal(t, tt.wantStatus, rec.Code)
			requireUIHeaders(t, rec.Header())

			body, err := io.ReadAll(rec.Body)
			require.NoError(t, err)

			if tt.wantBody != "" {
				require.Contains(t, string(body), tt.wantBody)
			}

			if tt.wantCache != "" {
				require.Equal(t, tt.wantCache, rec.Header().Get("Cache-Control"))
			}

			if tt.wantType != "" {
				require.Equal(t, tt.wantType, rec.Header().Get("Content-Type"))
			}

			if method == http.MethodHead {
				require.Empty(t, body)
			}
		})
	}
}

func TestHandlerLeavesReservedPathsToOthers(t *testing.T) {
	t.Parallel()

	handler, err := newHandler(testSite(), true)
	require.NoError(t, err)

	for _, prefix := range ReservedPrefixes {
		for _, urlPath := range []string{prefix, prefix + "/", prefix + "/anything/else"} {
			t.Run(urlPath, func(t *testing.T) {
				t.Parallel()

				rec := httptest.NewRecorder()
				handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, urlPath, nil))

				require.Equal(t, http.StatusNotFound, rec.Code)
				require.NotContains(t, rec.Body.String(), "root")
				require.Empty(t, rec.Header().Get("Content-Security-Policy"), "the UI does not answer here")
			})
		}
	}

	for _, urlPath := range []string{"/apiary", "/authors", "/hooksmith", "/metricsx"} {
		t.Run(urlPath, func(t *testing.T) {
			t.Parallel()

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, urlPath, nil))

			require.Equal(t, http.StatusOK, rec.Code, "only whole path segments are reserved")
		})
	}
}

func TestHandlerWithoutUI(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		site fstest.MapFS
	}{
		{name: "no site"},
		{name: "a site without index.html", site: fstest.MapFS{".gitkeep": {}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler, err := newHandler(tt.site, true)
			require.NoError(t, err)

			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil))

			require.Equal(t, http.StatusOK, rec.Code)
			require.Contains(t, rec.Body.String(), "built without the web UI")
			requireUIHeaders(t, rec.Header())
		})
	}
}

func requireUIHeaders(t *testing.T, header http.Header) {
	t.Helper()

	require.Equal(t, "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
		header.Get("Content-Security-Policy"))
	require.Equal(t, "nosniff", header.Get("X-Content-Type-Options"))
	require.Equal(t, "same-origin", header.Get("Referrer-Policy"))
}

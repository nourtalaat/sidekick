// Package web serves the single-page web UI that is built into the binary
// from web/. Every GET for a path that no other handler owns returns
// index.html, so the app's own router can take over, and the hashed files
// under /assets/ are cached for a year.
package web

import (
	"bytes"
	"embed"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// ReservedPrefixes are the paths other handlers own. The UI handler
// answers 404 under them instead of index.html, so a mistyped API path
// is never mistaken for a page. /metrics lives on the metrics listener,
// and stays here so that moving it cannot make the UI shadow it.
var ReservedPrefixes = []string{"/api", "/auth", "/hooks", "/slack", "/teams", "/healthz", "/readyz", "/metrics"}

const (
	contentSecurityPolicy = "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self' data:; font-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'"
	referrerPolicy        = "same-origin"
	noSniff               = "nosniff"

	// cacheImmutable is for files under /assets/, whose names carry a hash
	// of their content.
	cacheImmutable  = "public, max-age=31536000, immutable"
	cacheRevalidate = "no-cache"

	assetsPrefix = "/assets/"
	indexName    = "index.html"
)

//go:embed pages/*.html
var pages embed.FS

// contentTypes are the types of files that a minimal system, such as the
// distroless image, has no MIME database for.
var contentTypes = map[string]string{
	".woff2": "font/woff2",
	".woff":  "font/woff",
}

// Handler serves the web UI.
type Handler struct {
	site      fs.FS
	index     []byte
	needsOIDC []byte
	enabled   bool
}

// ServeHTTP answers GET and HEAD for the UI's paths.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if IsReserved(r.URL.Path) {
		http.NotFound(w, r)

		return
	}

	header := w.Header()
	header.Set("Content-Security-Policy", contentSecurityPolicy)
	header.Set("X-Content-Type-Options", noSniff)
	header.Set("Referrer-Policy", referrerPolicy)

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		header.Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)

		return
	}

	switch {
	case !h.enabled:
		serveBytes(w, r, h.needsOIDC)
	case strings.HasPrefix(r.URL.Path, assetsPrefix):
		h.serveAsset(w, r)
	default:
		h.servePage(w, r)
	}
}

// servePage serves a file of the site that sits next to index.html, such as
// the favicon, and index.html for every other path.
func (h *Handler) servePage(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
	if name != "" && name != indexName && h.site != nil {
		if file, ok := openFile(h.site, name); ok {
			defer func() { _ = file.Close() }()

			serveFile(w, r, name, file, cacheRevalidate)

			return
		}
	}

	serveBytes(w, r, h.index)
}

// serveAsset serves a hashed file, or 404 when there is none, since an old
// page that asks for a file of an earlier build must not get HTML back.
func (h *Handler) serveAsset(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")

	if h.site != nil {
		if file, ok := openFile(h.site, name); ok {
			defer func() { _ = file.Close() }()

			serveFile(w, r, name, file, cacheImmutable)

			return
		}
	}

	http.NotFound(w, r)
}

// New returns the handler of the UI built into the binary. With enabled
// false, which is the case when the config has no oidc section, every UI
// path answers with a page that says so. Without a UI in the binary, they
// answer with a page that says that.
func New(enabled bool) (*Handler, error) {
	return newHandler(embeddedSite(), enabled)
}

// IsReserved reports whether urlPath is under one of ReservedPrefixes.
func IsReserved(urlPath string) bool {
	for _, prefix := range ReservedPrefixes {
		if urlPath == prefix || strings.HasPrefix(urlPath, prefix+"/") {
			return true
		}
	}

	return false
}

func newHandler(site fs.FS, enabled bool) (*Handler, error) {
	needsOIDC, err := pages.ReadFile("pages/needs-oidc.html")
	if err != nil {
		return nil, fmt.Errorf("read the page for a config without oidc: %w", err)
	}

	var index []byte

	if site != nil {
		index, err = fs.ReadFile(site, indexName)
		if err != nil {
			site = nil
		}
	}

	if site == nil {
		index, err = pages.ReadFile("pages/no-ui.html")
		if err != nil {
			return nil, fmt.Errorf("read the page for a build without the UI: %w", err)
		}
	}

	return &Handler{site: site, index: index, needsOIDC: needsOIDC, enabled: enabled}, nil
}

// openFile opens the regular file name of site.
func openFile(site fs.FS, name string) (fs.File, bool) {
	if !fs.ValidPath(name) {
		return nil, false
	}

	file, err := site.Open(name)
	if err != nil {
		return nil, false
	}

	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = file.Close()

		return nil, false
	}

	return file, true
}

func serveFile(w http.ResponseWriter, r *http.Request, name string, file fs.File, cacheControl string) {
	content, ok := file.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(file)
		if err != nil {
			http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)

			return
		}

		content = bytes.NewReader(data)
	}

	w.Header().Set("Cache-Control", cacheControl)

	if contentType, ok := contentTypes[strings.ToLower(path.Ext(name))]; ok {
		w.Header().Set("Content-Type", contentType)
	}

	http.ServeContent(w, r, name, time.Time{}, content)
}

// serveBytes serves an HTML page, which the browser must check again each
// time.
func serveBytes(w http.ResponseWriter, r *http.Request, page []byte) {
	w.Header().Set("Cache-Control", cacheRevalidate)
	http.ServeContent(w, r, indexName, time.Time{}, bytes.NewReader(page))
}

package gitlab

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/buildinfo"
	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Limits of the client.
const (
	// maxResponseSize bounds one response body. The largest lists are
	// merge requests with long descriptions; results are cut much smaller
	// later.
	maxResponseSize = 8 << 20
	// maxErrorBodySize bounds the part of an error answer that is read for
	// GitLab's message.
	maxErrorBodySize = 64 << 10
	// maxRedirects is the most redirects one request follows. GitLab
	// redirects a request for a project that moved to its new address.
	maxRedirects = 3
)

var (
	// errResponseTooLarge is returned when a response body is larger than
	// its limit.
	errResponseTooLarge = errors.New("the response from GitLab is larger than the limit")
	// errEmptyToken is returned when the token secret holds nothing.
	errEmptyToken = errors.New("the token secret is empty")
	// errRedirect is returned when GitLab redirects a request somewhere the
	// client does not follow, so the token never goes to another host or
	// over plain http.
	errRedirect = errors.New("GitLab redirected the request to another address")
)

// linkPattern finds one link of a Link header, with its relation.
var linkPattern = regexp.MustCompile(`<([^>]*)>[^,<]*?rel="?([a-z]+)"?`)

// statusError is an answer of GitLab that is not a success.
type statusError struct {
	code int
	// message is GitLab's own error message, untrusted.
	message string
}

// Error returns the status and GitLab's message.
func (e *statusError) Error() string {
	text := fmt.Sprintf("GitLab answered %d", e.code)
	if e.message != "" {
		text += ": " + e.message
	}

	return text
}

// client sends requests to the REST API with the source's token.
type client struct {
	http      *http.Client
	apiURL    string
	userAgent string
	token     registry.SecretRef
	secrets   registry.Secrets
}

// get sends a GET request for path, under the API root, with query, and
// decodes the JSON answer into out. It returns the response headers. An
// answer that is not a success returns a *statusError. The path must
// already be escaped, as projectPath builds it.
func (c *client) get(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	return c.getLimited(ctx, path, query, out, maxResponseSize)
}

// getLimited is get with a limit on the size of the body. A longer body
// returns errResponseTooLarge.
func (c *client) getLimited(ctx context.Context, path string, query url.Values, out any, limit int64) (http.Header, error) {
	token, err := c.readToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the token: %w", err)
	}

	target := c.apiURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("User-Agent", c.userAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send the request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, readStatusError(resp.StatusCode, resp.Body)
	}

	body := httpclient.LimitBody(resp.Body, limit, errResponseTooLarge)

	if err := json.NewDecoder(body).Decode(out); err != nil {
		return nil, fmt.Errorf("decode the answer: %w", err)
	}

	return resp.Header, nil
}

// readToken returns the token from its secret, read on every request so a
// rotated token needs no restart.
func (c *client) readToken(ctx context.Context) (string, error) {
	raw, err := c.secrets.Resolve(ctx, c.token)
	if err != nil {
		return "", fmt.Errorf("resolve secret %s/%s: %w", c.token.Name, c.token.Key, err)
	}

	token := string(bytes.TrimSpace(raw))
	if token == "" {
		return "", errEmptyToken
	}

	return token, nil
}

// newClient returns the client of a source. It follows a redirect only to
// the same scheme and host as the GitLab root, because Go would send the
// token header to any host a redirect names.
func newClient(cfg Config, deps registry.Deps) (*client, error) {
	root, err := url.Parse(cfg.baseURL())
	if err != nil {
		return nil, fmt.Errorf("parse the base URL: %w", err)
	}

	httpClient := deps.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	own := *httpClient
	own.CheckRedirect = sameHostRedirects(root)

	return &client{
		http:      &own,
		apiURL:    cfg.baseURL() + apiSuffix,
		userAgent: "sidekick/" + buildinfo.Version,
		token:     cfg.TokenSecret,
		secrets:   deps.Secrets,
	}, nil
}

// sameHostRedirects returns the redirect policy of the client: at most
// maxRedirects redirects, all to the scheme and host of root.
func sameHostRedirects(root *url.URL) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return fmt.Errorf("more than %d redirects: %w", maxRedirects, errRedirect)
		}

		if req.URL.Scheme != root.Scheme || req.URL.Host != root.Host {
			return fmt.Errorf("redirect to %s://%s: %w", req.URL.Scheme, req.URL.Host, errRedirect)
		}

		return nil
	}
}

// listPage reads one page of a list: page number page, perPage items. It
// returns the items and the number of the next page, or 0 when this was the
// last.
func listPage[T any](ctx context.Context, c *client, path string, query url.Values, page, perPage int) ([]T, int, error) {
	q := maps.Clone(query)
	if q == nil {
		q = url.Values{}
	}

	q.Set("page", strconv.Itoa(page))
	q.Set("per_page", strconv.Itoa(perPage))

	var items []T

	header, err := c.get(ctx, path, q, &items)
	if err != nil {
		return nil, 0, fmt.Errorf("get %s: %w", path, err)
	}

	return items, nextPage(header), nil
}

// nextPage returns the number of the next page a list answer points to, or
// 0. It reads the x-next-page header and, when that is empty, the page of
// the next link of the Link header. It never uses the host of a link.
func nextPage(h http.Header) int {
	if n, err := strconv.Atoi(strings.TrimSpace(h.Get("X-Next-Page"))); err == nil && n > 0 {
		return n
	}

	for _, link := range h.Values("Link") {
		for _, m := range linkPattern.FindAllStringSubmatch(link, -1) {
			if m[2] != "next" {
				continue
			}

			u, err := url.Parse(m[1])
			if err != nil {
				return 0
			}

			if n, err := strconv.Atoi(u.Query().Get("page")); err == nil && n > 0 {
				return n
			}
		}
	}

	return 0
}

// projectPath returns the API path of a project's resource: parts follow
// the project and must already be escaped. The project's full path is one
// escaped part, group%2Fproject, as GitLab expects.
func projectPath(project string, parts ...string) string {
	path := "/projects/" + url.PathEscape(project)
	if len(parts) > 0 {
		path += "/" + strings.Join(parts, "/")
	}

	return path
}

// readStatusError returns the *statusError of an answer with code, with
// GitLab's message when the body holds one. The message is a string or an
// object, and OAuth style errors use error and error_description.
func readStatusError(code int, body io.Reader) error {
	e := &statusError{code: code}

	data, err := io.ReadAll(io.LimitReader(body, maxErrorBodySize))
	if err != nil {
		return e
	}

	var parsed struct {
		Message          json.RawMessage `json:"message"`
		Error            json.RawMessage `json:"error"`
		ErrorDescription string          `json:"error_description"`
	}

	if json.Unmarshal(data, &parsed) != nil {
		return e
	}

	switch {
	case len(parsed.Message) > 0:
		e.message = source.CutText(textOf(parsed.Message), maxTitleLen)
	case parsed.ErrorDescription != "":
		e.message = source.CutText(parsed.ErrorDescription, maxTitleLen)
	case len(parsed.Error) > 0:
		e.message = source.CutText(textOf(parsed.Error), maxTitleLen)
	}

	return e
}

// textOf returns a JSON string as its text, and any other JSON value as
// compact JSON.
func textOf(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}

	var compact bytes.Buffer
	if json.Compact(&compact, raw) != nil {
		return ""
	}

	return compact.String()
}

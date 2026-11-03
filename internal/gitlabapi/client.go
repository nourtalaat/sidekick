// Package gitlabapi is the REST client that the GitLab source and the GitLab
// actions share: a plain HTTP client that sends the access token in the
// PRIVATE-TOKEN header, follows a redirect of a read only to the same host,
// and builds API paths from a project's full path.
//
// The token is read from its secret on every request, so a rotated token
// needs no restart. A request that carries a body never follows a redirect:
// Go would turn a redirected POST into a GET, and a write must not go
// anywhere the config did not name.
package gitlabapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
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
	// maxMessageBytes bounds the message of an error answer, which is
	// untrusted text.
	maxMessageBytes = 512
	// maxRedirects is the most redirects one request follows. GitLab
	// redirects a request for a project that moved to its new address.
	maxRedirects = 3
)

var (
	// ErrResponseTooLarge is returned when a response body is larger than
	// its limit.
	ErrResponseTooLarge = errors.New("the response from GitLab is larger than the limit")
	// errEmptyToken is returned when the token secret holds nothing.
	errEmptyToken = errors.New("the token secret is empty")
	// errRedirect is returned when GitLab redirects a request somewhere the
	// client does not follow, so the token never goes to another host or
	// over plain http.
	errRedirect = errors.New("GitLab redirected the request to another address")

	errNoSecrets = errors.New("no secrets resolver in deps")
)

// Options set up a Client.
type Options struct {
	// BaseURL is the root of the GitLab. Empty means gitlab.com.
	BaseURL string
	// Token names the secret that holds the access token.
	Token registry.SecretRef
	// Secrets resolves Token. It is required.
	Secrets registry.Secrets
	// HTTP is the HTTP client to send with. Nil means httpclient.New().
	HTTP *http.Client
	// UserAgent is sent with every request.
	UserAgent string
}

// Client sends requests to the REST API with an access token. It is safe for
// concurrent use.
type Client struct {
	http      *http.Client
	apiURL    string
	userAgent string
	token     registry.SecretRef
	secrets   registry.Secrets
}

// New returns a Client. It follows a redirect of a read only to the same
// scheme and host as the GitLab root, because Go would send the token header
// to any host a redirect names. It makes no network call.
func New(opts Options) (*Client, error) {
	if opts.Secrets == nil {
		return nil, errNoSecrets
	}

	root, err := url.Parse(BaseURL(opts.BaseURL))
	if err != nil {
		return nil, fmt.Errorf("parse the base URL: %w", err)
	}

	httpClient := opts.HTTP
	if httpClient == nil {
		httpClient = httpclient.New()
	}

	own := *httpClient
	own.CheckRedirect = redirects(root)

	return &Client{
		http:      &own,
		apiURL:    BaseURL(opts.BaseURL) + apiSuffix,
		userAgent: opts.UserAgent,
		token:     opts.Token,
		secrets:   opts.Secrets,
	}, nil
}

// Get sends a GET request for path, under the API root, with query, and
// decodes the JSON answer into out. It returns the response headers. An
// answer that is not a success returns a *StatusError. The path must already
// be escaped, as ProjectPath builds it.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) (http.Header, error) {
	return c.GetLimited(ctx, path, query, out, maxResponseSize)
}

// GetLimited is Get with a limit on the size of the body. A longer body
// returns ErrResponseTooLarge.
func (c *Client) GetLimited(ctx context.Context, path string, query url.Values, out any, limit int64) (http.Header, error) {
	target := c.apiURL + path
	if len(query) > 0 {
		target += "?" + query.Encode()
	}

	return c.do(ctx, http.MethodGet, target, nil, out, limit)
}

// Send sends a request with a JSON body to path, under the API root, and
// decodes the JSON answer into out, which may be nil for an answer with no
// body. A nil body sends none. An answer that is not a success returns a
// *StatusError, and a redirect is never followed.
func (c *Client) Send(ctx context.Context, method, path string, body, out any) error {
	var payload []byte

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encode the request: %w", err)
		}

		payload = raw
	}

	if _, err := c.do(ctx, method, c.apiURL+path, payload, out, maxResponseSize); err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}

	return nil
}

// do sends one request and decodes the answer.
func (c *Client) do(ctx context.Context, method, target string, payload []byte, out any, limit int64) (http.Header, error) {
	token, err := c.readToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("read the token: %w", err)
	}

	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("make the request: %w", err)
	}

	req.Header.Set("Accept", "application/json")
	req.Header.Set("PRIVATE-TOKEN", token)
	req.Header.Set("User-Agent", c.userAgent)

	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send the request: %w", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return nil, readStatusError(resp.StatusCode, resp.Body)
	}

	if out == nil {
		return resp.Header, nil
	}

	limited := httpclient.LimitBody(resp.Body, limit, ErrResponseTooLarge)

	if err := json.NewDecoder(limited).Decode(out); err != nil {
		return nil, fmt.Errorf("decode the answer: %w", err)
	}

	return resp.Header, nil
}

// readToken returns the token from its secret, read on every request so a
// rotated token needs no restart.
func (c *Client) readToken(ctx context.Context) (string, error) {
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

// redirects returns the redirect policy of the client. A request that is not
// a GET never follows a redirect, and its 3xx answer is returned as it is. A
// GET follows at most maxRedirects redirects, all to the scheme and host of
// root.
func redirects(root *url.URL) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if via[0].Method != http.MethodGet {
			return http.ErrUseLastResponse
		}

		if len(via) > maxRedirects {
			return fmt.Errorf("more than %d redirects: %w", maxRedirects, errRedirect)
		}

		if req.URL.Scheme != root.Scheme || req.URL.Host != root.Host {
			return fmt.Errorf("redirect to %s://%s: %w", req.URL.Scheme, req.URL.Host, errRedirect)
		}

		return nil
	}
}

// ProjectPath returns the API path of a project's resource: parts follow the
// project and must already be escaped. The project's full path is one escaped
// part, group%2Fproject, as GitLab expects.
func ProjectPath(project string, parts ...string) string {
	path := "/projects/" + url.PathEscape(project)
	if len(parts) > 0 {
		path += "/" + strings.Join(parts, "/")
	}

	return path
}

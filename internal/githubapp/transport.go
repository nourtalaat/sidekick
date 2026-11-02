package githubapp

import (
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/nourtalaat/sidekick/internal/httpclient"
)

// maxDiscard is the most of an unwanted response body that is read before
// its connection is closed.
const maxDiscard = 64 << 10

// maxResponseSize bounds one response body. The largest answers are
// commits with many patches and files of up to a megabyte, which the
// contents API sends in base64; results are cut much smaller later.
const maxResponseSize = 16 << 20

// errResponseTooLarge is returned when a response body is larger than
// maxResponseSize.
var errResponseTooLarge = fmt.Errorf("GitHub sent a response larger than %d bytes", maxResponseSize)

// limitTransport fails the read of any response body larger than
// maxResponseSize, so a huge answer cannot make Sidekick read without end.
type limitTransport struct {
	next http.RoundTripper
}

// RoundTrip implements http.RoundTripper.
func (t limitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	resp.Body = httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)

	return resp, nil
}

// authTransport adds the installation token to every request to the API
// origin, and leaves requests to any other origin without it. When GitHub
// answers 401 to a request that is safe to repeat, it drops the token and
// sends the request once more with a new one, which covers a token that
// was revoked before it expired.
type authTransport struct {
	next   http.RoundTripper
	origin *url.URL
	tokens *tokenSource
}

// RoundTrip implements http.RoundTripper.
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if !sameOrigin(req.URL, t.origin) {
		resp, err := t.send(req, "")
		if err != nil {
			return nil, fmt.Errorf("send without a token: %w", err)
		}

		return resp, nil
	}

	resp, token, err := t.authorized(req)
	if err != nil {
		return nil, fmt.Errorf("send with the installation token: %w", err)
	}

	if resp.StatusCode != http.StatusUnauthorized || (req.Method != http.MethodGet && req.Method != http.MethodHead) {
		return resp, nil
	}

	discard(resp)
	t.tokens.Invalidate(token)

	resp, _, err = t.authorized(req)
	if err != nil {
		return nil, fmt.Errorf("send again with a new installation token: %w", err)
	}

	return resp, nil
}

// authorized sends req with the current token and returns the token it
// used.
func (t *authTransport) authorized(req *http.Request) (*http.Response, string, error) {
	token, err := t.tokens.Token(req.Context())
	if err != nil {
		return nil, "", fmt.Errorf("authenticate as the GitHub App: %w", err)
	}

	resp, err := t.send(req, token)
	if err != nil {
		return nil, "", fmt.Errorf("send: %w", err)
	}

	return resp, token, nil
}

// send sends a copy of req with token as its bearer token, or without an
// Authorization header when token is empty.
func (t *authTransport) send(req *http.Request, token string) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.Header.Del("Authorization")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	return resp, nil
}

// sameOrigin reports whether u has the scheme and host of origin.
func sameOrigin(u, origin *url.URL) bool {
	return u.Scheme == origin.Scheme && u.Host == origin.Host
}

// discard reads what is left of a response body and closes it, so its
// connection can be reused.
func discard(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxDiscard))
	_ = resp.Body.Close()
}

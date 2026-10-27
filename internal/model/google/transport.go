package google

import (
	"context"
	"fmt"
	"net/http"

	"cloud.google.com/go/auth"

	"github.com/nourtalaat/sidekick/internal/httpclient"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
)

// maxResponseSize bounds one response body. A streamed answer of the
// largest output limit, with its thought signatures and JSON framing, stays
// well below it.
const maxResponseSize = 64 << 20

// Headers the transport sets.
const (
	apiKeyHeader      = "x-goog-api-key"
	userProjectHeader = "X-Goog-User-Project"
	authHeader        = "Authorization"
)

// errResponseTooLarge is returned when a response body is larger than
// maxResponseSize.
var errResponseTooLarge = fmt.Errorf("google sent a response larger than %d bytes", maxResponseSize)

// tokenSource gives OAuth access tokens. *auth.Credentials implements it,
// and caches and refreshes the token.
type tokenSource interface {
	Token(ctx context.Context) (*auth.Token, error)
}

// credentialError is a failure to get the credentials of a call: the API
// key or an access token. classify reports it as an auth error.
type credentialError struct {
	err error
}

// Error implements error.
func (e *credentialError) Error() string {
	return "get credentials: " + e.err.Error()
}

// Unwrap returns the cause.
func (e *credentialError) Unwrap() error {
	return e.err
}

// authTransport adds the credentials to every request, read when the
// request is sent so a rotated key or a refreshed token is used, and limits
// the size of every response body. With backend gemini it sets the API key
// header; with vertex, a bearer token and the quota project.
type authTransport struct {
	next http.RoundTripper

	// secrets and apiKey give the Gemini API key.
	secrets registry.Secrets
	apiKey  registry.SecretRef

	// tokens and quotaProject give the Vertex AI credentials.
	tokens       tokenSource
	quotaProject string
}

// RoundTrip implements http.RoundTripper. It changes a copy of the request,
// as the http.RoundTripper contract asks.
func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())

	if err := t.authorize(req); err != nil {
		return nil, &credentialError{err: err}
	}

	resp, err := t.next.RoundTrip(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}

	resp.Body = httpclient.LimitBody(resp.Body, maxResponseSize, errResponseTooLarge)

	return resp, nil
}

// authorize sets the credential headers on req.
func (t *authTransport) authorize(req *http.Request) error {
	if t.tokens == nil {
		key, err := secrets.ResolveText(req.Context(), t.secrets, t.apiKey)
		if err != nil {
			return fmt.Errorf("resolve %s/%s: %w", t.apiKey.Name, t.apiKey.Key, err)
		}

		req.Header.Set(apiKeyHeader, key)

		return nil
	}

	tok, err := t.tokens.Token(req.Context())
	if err != nil {
		return fmt.Errorf("get access token: %w", err)
	}

	req.Header.Del(apiKeyHeader)
	req.Header.Set(authHeader, tokenType(tok)+" "+tok.Value)

	if t.quotaProject != "" {
		req.Header.Set(userProjectHeader, t.quotaProject)
	}

	return nil
}

// tokenType returns the token's type, Bearer when it has none.
func tokenType(tok *auth.Token) string {
	if tok.Type == "" {
		return "Bearer"
	}

	return tok.Type
}

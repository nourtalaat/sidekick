package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	gh "github.com/google/go-github/v93/github"

	"github.com/nourtalaat/sidekick/internal/registry"
)

// Timings of the GitHub App authentication.
const (
	// jwtBackdate moves the JWT's issue time into the past, so a GitHub
	// clock that is a little behind still accepts it.
	jwtBackdate = 60 * time.Second
	// jwtLifetime is how long after now the JWT expires. GitHub refuses an
	// expiry more than 10 minutes ahead; with the backdated issue time this
	// keeps the whole JWT within 10 minutes.
	jwtLifetime = 9 * time.Minute
	// refreshBefore is how long before its expiry an installation token is
	// replaced, so no call starts with a token that runs out on the way.
	refreshBefore = 5 * time.Minute
)

// maxDiscard is the most of an unwanted response body that is read before
// its connection is closed.
const maxDiscard = 64 << 10

// readPermission is the access level of every permission Sidekick asks for.
const readPermission = "read"

// errNoToken is returned when GitHub answers the token exchange without a
// token or without its expiry time.
var errNoToken = errors.New("GitHub returned no installation token or no expiry time")

// installationTokens makes and caches the installation access token of one
// GitHub App installation. It reads the app's private key through the
// secrets resolver on each exchange, so a rotated key is used without a
// restart. It is safe for concurrent use: when several calls find the token
// expired, one exchange runs and the others wait for its token.
type installationTokens struct {
	appID          int64
	installationID int64
	privateKey     registry.SecretRef
	secrets        registry.Secrets
	// client sends the exchange to baseURL, the REST API root. It must not
	// add a token of its own.
	client    *http.Client
	baseURL   string
	userAgent string
	now       func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

// Token returns a token that is valid for at least refreshBefore, making a
// new one when the cached one is missing or about to expire.
func (t *installationTokens) Token(ctx context.Context) (string, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token != "" && t.now().Before(t.expires.Add(-refreshBefore)) {
		return t.token, nil
	}

	token, expires, err := t.exchange(ctx)
	if err != nil {
		return "", fmt.Errorf("make an installation token: %w", err)
	}

	t.token, t.expires = token, expires

	return token, nil
}

// Invalidate drops token from the cache, so the next call to Token makes a
// new one. It does nothing when the cache already holds another token,
// because another call has replaced it.
func (t *installationTokens) Invalidate(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token == token {
		t.token = ""
	}
}

// exchange signs an app JWT and trades it for an installation token that
// can only read.
func (t *installationTokens) exchange(ctx context.Context) (string, time.Time, error) {
	signed, err := t.appJWT(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign the app JWT: %w", err)
	}

	client, err := t.exchangeClient(signed)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("make the exchange client: %w", err)
	}

	tok, _, err := client.Apps.CreateInstallationToken(ctx, t.installationID, &gh.InstallationTokenOptions{
		Permissions: readOnlyPermissions(),
	})
	if err != nil {
		return "", time.Time{}, fmt.Errorf("exchange the app JWT for installation %d: %w", t.installationID, err)
	}

	if tok.GetToken() == "" || tok.GetExpiresAt().IsZero() {
		return "", time.Time{}, errNoToken
	}

	return tok.GetToken(), tok.GetExpiresAt().Time, nil
}

// exchangeClient returns a go-github client that sends the app JWT. It
// works on a copy of the HTTP client, because go-github changes the
// transport of the client it is given.
func (t *installationTokens) exchangeClient(signed string) (*gh.Client, error) {
	httpClient := *t.client

	client, err := gh.NewClient(
		gh.WithHTTPClient(&httpClient),
		gh.WithURLs(&t.baseURL, nil),
		gh.WithAuthToken(signed),
		gh.WithUserAgent(t.userAgent),
	)
	if err != nil {
		return nil, fmt.Errorf("make the go-github client: %w", err)
	}

	return client, nil
}

// appJWT reads the private key and signs a JWT that identifies the app.
func (t *installationTokens) appJWT(ctx context.Context) (string, error) {
	pem, err := t.secrets.Resolve(ctx, t.privateKey)
	if err != nil {
		return "", fmt.Errorf("read the private key: %w", err)
	}

	key, err := jwt.ParseRSAPrivateKeyFromPEM(pem)
	if err != nil {
		return "", fmt.Errorf("parse the private key: %w", err)
	}

	now := t.now()
	claims := jwt.RegisteredClaims{
		Issuer:    strconv.FormatInt(t.appID, 10),
		IssuedAt:  jwt.NewNumericDate(now.Add(-jwtBackdate)),
		ExpiresAt: jwt.NewNumericDate(now.Add(jwtLifetime)),
	}

	signed, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		return "", fmt.Errorf("sign: %w", err)
	}

	return signed, nil
}

// authTransport adds the installation token to every request to the API
// origin, and leaves requests to any other origin without it. When GitHub
// answers 401 to a request that is safe to repeat, it drops the token and
// sends the request once more with a new one, which covers a token that
// was revoked before it expired.
type authTransport struct {
	next   http.RoundTripper
	origin *url.URL
	tokens *installationTokens
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

// readOnlyPermissions are the permissions an installation token is asked
// for. A token never gets more than these, even when the app was given
// more.
func readOnlyPermissions() *gh.InstallationPermissions {
	read := readPermission

	return &gh.InstallationPermissions{
		Actions:      &read,
		Contents:     &read,
		Metadata:     &read,
		PullRequests: &read,
	}
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

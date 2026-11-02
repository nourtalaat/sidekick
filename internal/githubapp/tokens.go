package githubapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

// errNoToken is returned when GitHub answers the token exchange without a
// token or without its expiry time.
var errNoToken = errors.New("GitHub returned no installation token or no expiry time")

// tokenSource makes and caches the installation access token of one
// GitHub App installation. It reads the app's private key through the
// secrets resolver on each exchange, so a rotated key is used without a
// restart. It is safe for concurrent use: when several calls find the token
// expired, one exchange runs and the others wait for its token.
type tokenSource struct {
	appID          int64
	installationID int64
	privateKey     registry.SecretRef
	permissions    *gh.InstallationPermissions
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
func (t *tokenSource) Token(ctx context.Context) (string, error) {
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
func (t *tokenSource) Invalidate(token string) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.token == token {
		t.token = ""
	}
}

// exchange signs an app JWT and trades it for an installation token that
// can only read.
func (t *tokenSource) exchange(ctx context.Context) (string, time.Time, error) {
	signed, err := t.appJWT(ctx)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign the app JWT: %w", err)
	}

	client, err := t.exchangeClient(signed)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("make the exchange client: %w", err)
	}

	tok, _, err := client.Apps.CreateInstallationToken(ctx, t.installationID, &gh.InstallationTokenOptions{
		Permissions: t.permissions,
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
func (t *tokenSource) exchangeClient(signed string) (*gh.Client, error) {
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
func (t *tokenSource) appJWT(ctx context.Context) (string, error) {
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

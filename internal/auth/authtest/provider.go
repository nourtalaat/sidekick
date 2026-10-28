// Package authtest is a fake OpenID Connect provider for tests of web UI
// login, on internal/fakehttp. It serves discovery, signing keys and the
// token endpoint, checks PKCE, and signs ID tokens with claims the test
// chooses.
package authtest

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
)

// The paths of the fake provider.
const (
	DiscoveryPath = "/.well-known/openid-configuration"
	AuthorizePath = "/authorize"
	TokenPath     = "/token"
	KeysPath      = "/keys"
)

// keyID names the provider's one signing key.
const keyID = "test-key"

// rsaKeyBits is the size of the signing key.
const rsaKeyBits = 2048

// tokenLifetime is how long the ID tokens the provider signs last.
const tokenLifetime = 5 * time.Minute

// Claims are the claims of an ID token.
type Claims map[string]any

// Provider is a fake OIDC provider that lives for one test.
type Provider struct {
	*fakehttp.Server

	t        testing.TB
	clientID string
	key      *rsa.PrivateKey

	mu sync.Mutex
	// pending holds what Authorize saw, by the code it handed out.
	pending map[string]authorization
	// claims makes the claims of the next ID tokens from the defaults.
	claims func(Claims)
}

// authorization is one login at the provider, waiting for its code to be
// traded for tokens.
type authorization struct {
	nonce     string
	challenge string
	redirect  string
}

// NewProvider starts a provider for clientID. Its issuer is its URL.
func NewProvider(t testing.TB, clientID string) *Provider {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, rsaKeyBits)
	require.NoError(t, err)

	p := &Provider{Server: fakehttp.New(t), t: t, clientID: clientID, key: key, pending: map[string]authorization{}}

	p.HandleFunc(http.MethodGet, DiscoveryPath, p.discovery)
	p.HandleFunc(http.MethodGet, KeysPath, p.keys)
	p.HandleFunc(http.MethodPost, TokenPath, p.token)

	return p
}

// Issuer returns the provider's issuer URL.
func (p *Provider) Issuer() string {
	return p.URL()
}

// SetClaims makes edit change the claims of the ID tokens signed from now
// on. The defaults are iss, aud, sub ("user-1"), iat, exp, the nonce of
// the login, email, email_verified, name and groups.
func (p *Provider) SetClaims(edit func(Claims)) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.claims = edit
}

// Authorize plays the user signing in at the provider: it checks the
// authorization request that location, the redirect /auth/login answers
// with, makes, and returns the query the provider sends the browser back
// to the callback with.
func (p *Provider) Authorize(location string) url.Values {
	p.t.Helper()

	u, err := url.Parse(location)
	require.NoError(p.t, err)
	require.Equal(p.t, p.URL()+AuthorizePath, u.Scheme+"://"+u.Host+u.Path)

	q := u.Query()
	require.Equal(p.t, "code", q.Get("response_type"))
	require.Equal(p.t, p.clientID, q.Get("client_id"))
	require.Equal(p.t, "S256", q.Get("code_challenge_method"))
	require.NotEmpty(p.t, q.Get("code_challenge"))
	require.NotEmpty(p.t, q.Get("nonce"))
	require.NotEmpty(p.t, q.Get("state"))
	require.Contains(p.t, q.Get("scope"), "openid")

	code := rand.Text()

	p.mu.Lock()
	p.pending[code] = authorization{nonce: q.Get("nonce"), challenge: q.Get("code_challenge"), redirect: q.Get("redirect_uri")}
	p.mu.Unlock()

	return url.Values{"code": {code}, "state": {q.Get("state")}}
}

func (p *Provider) discovery(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"issuer":                                p.URL(),
		"authorization_endpoint":                p.URL() + AuthorizePath,
		"token_endpoint":                        p.URL() + TokenPath,
		"jwks_uri":                              p.URL() + KeysPath,
		"response_types_supported":              []string{"code"},
		"subject_types_supported":               []string{"public"},
		"id_token_signing_alg_values_supported": []string{"RS256"},
	})
}

func (p *Provider) keys(w http.ResponseWriter, _ *http.Request) {
	pub := p.key.PublicKey

	writeJSON(w, http.StatusOK, map[string]any{"keys": []map[string]string{{
		"kty": "RSA", "kid": keyID, "use": "sig", "alg": "RS256",
		"n": base64.RawURLEncoding.EncodeToString(pub.N.Bytes()),
		"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes()),
	}}})
}

// token trades a code from Authorize for tokens, when the code verifier
// matches the challenge and the redirect URI is the one the login used.
func (p *Provider) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})

		return
	}

	p.mu.Lock()
	auth, ok := p.pending[r.PostForm.Get("code")]
	delete(p.pending, r.PostForm.Get("code"))
	edit := p.claims
	p.mu.Unlock()

	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))

	switch {
	case !ok, r.PostForm.Get("grant_type") != "authorization_code", r.PostForm.Get("redirect_uri") != auth.redirect:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})

		return
	case base64.RawURLEncoding.EncodeToString(sum[:]) != auth.challenge:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant", "error_description": "PKCE check failed"})

		return
	}

	now := time.Now()
	claims := Claims{
		"iss": p.URL(), "aud": p.clientID, "sub": "user-1", "iat": now.Unix(), "exp": now.Add(tokenLifetime).Unix(),
		"nonce": auth.nonce, "email": "ana@example.com", "email_verified": true, "name": "Ana", "groups": []string{"sre-admins"},
	}

	if edit != nil {
		edit(claims)
	}

	idToken := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims(claims))
	idToken.Header["kid"] = keyID

	signed, err := idToken.SignedString(p.key)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "server_error"})

		return
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"access_token": rand.Text(), "token_type": "Bearer", "expires_in": int(tokenLifetime.Seconds()), "id_token": signed,
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

//go:build integration

package api_test

import (
	"bytes"
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/api"
	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/auth/authtest"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// sessionCookie is the name of the session cookie.
const sessionCookie = "__Host-sidekick_session"

// The sessions of fixture/sessions.sql, as cookie values, and the CSRF
// tokens of the live ones.
var (
	viSession        = sessionValue(0x01)
	viExpiredSession = sessionValue(0x02)
	adSession        = sessionValue(0x03)
)

const (
	viCSRF = "csrf-vi"
	adCSRF = "csrf-ad"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// server is the login endpoints and the API, wired as sidekick serve
// wires them, on a database of their own seeded with fixture/sessions.sql
// and fixture/rules.sql, with a fake OIDC provider.
type server struct {
	mux      *http.ServeMux
	provider *authtest.Provider
	pool     *pgxpool.Pool
}

func setup(t *testing.T) server {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/sessions.sql")
	storetest.LoadFixture(t, pool, "fixture/rules.sql")

	provider := authtest.NewProvider(t, "sidekick")

	cfg := &config.Config{
		Server:   config.Server{PublicURL: "https://sidekick.example.com", Listen: ":8080"},
		Database: config.Database{URLSecret: registry.SecretRef{Name: "sidekick-db", Key: "url"}, MaxConns: 20},
		OIDC: &config.OIDC{
			Issuer:       provider.Issuer(),
			ClientID:     "sidekick",
			ClientSecret: registry.SecretRef{Name: "sidekick-oidc", Key: "clientSecret"},
			GroupsClaim:  config.DefaultGroupsClaim,
			Roles: map[config.RoleName][]string{
				config.RoleAdmin:  {"sre-admins", "lead@example.com"},
				config.RoleViewer: {config.AnyGroup},
			},
		},
	}

	redactor, err := redact.New(redact.Options{BuiltIn: []string{"emails"}, HashKey: []byte("test-key")})
	require.NoError(t, err)

	sessions, err := auth.New(auth.Options{
		OIDC:      cfg.OIDC,
		PublicURL: cfg.Server.PublicURL,
		Secrets:   staticSecrets{},
		Pool:      pool,
		Audit:     audit.NewWriter(),
		HTTP:      provider.Client(),
	})
	require.NoError(t, err)

	handler, err := api.New(api.Options{
		Auth: sessions, Tokens: sessions, Config: cfg, Rules: rules.NewStore(pool, cfg), Redactor: redactor,
		Pool: pool, Audit: audit.NewWriter(),
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	sessions.Register(mux)
	mux.Handle(api.BasePath+"/", handler)

	return server{mux: mux, provider: provider, pool: pool}
}

// do sends a request with the session cookie, when it is set, and the CSRF
// header, when csrf is set.
func (s server) do(t *testing.T, method, path, session, csrf string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, body)
	if session != "" {
		req.AddCookie(newSessionCookie(session))
	}

	if csrf != "" {
		req.Header.Set(auth.CSRFHeader, csrf)
	}

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	return rec
}

// doWithToken sends a request with the API token in the Authorization
// header.
func (s server) doWithToken(t *testing.T, method, path, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), method, path, body)
	req.Header.Set("Authorization", "Bearer "+token)

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)

	return rec
}

// login signs in through the fake provider and returns the session cookie
// value.
func (s server) login(t *testing.T) string {
	t.Helper()

	start := s.do(t, http.MethodGet, auth.LoginPath, "", "", nil)
	require.Equal(t, http.StatusFound, start.Code)

	query := s.provider.Authorize(start.Header().Get("Location"))

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, auth.CallbackPath+"?"+query.Encode(), nil)
	for _, c := range start.Result().Cookies() {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	s.mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusSeeOther, rec.Code, rec.Body.String())

	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c.Value
		}
	}

	require.FailNow(t, "the callback set no session cookie")

	return ""
}

// staticSecrets resolves every reference to the same fake client secret.
type staticSecrets struct{}

func (staticSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte("client-secret\n"), nil
}

// sessionValue returns the cookie value of a session ID of 32 bytes of b.
func sessionValue(b byte) string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{b}, 32))
}

// newSessionCookie returns the session cookie with value, with the
// attributes the server sets on it.
func newSessionCookie(value string) *http.Cookie {
	return &http.Cookie{Name: sessionCookie, Value: value, Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
}

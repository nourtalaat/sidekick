//go:build integration

package auth_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/audit"
	"github.com/nourtalaat/sidekick/internal/auth"
	"github.com/nourtalaat/sidekick/internal/auth/authtest"
	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The client of Sidekick at the fake provider, and where Sidekick is.
const (
	testClientID  = "sidekick"
	testPublicURL = "https://sidekick.example.com"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// env is one test's Service, on a database of its own, with a fake
// provider.
type env struct {
	svc      *auth.Service
	mux      *http.ServeMux
	pool     *pgxpool.Pool
	provider *authtest.Provider
}

// setup returns an env seeded with fixture/users.sql. Users in sre-admins
// are admins, in engineering responders, and nobody else has a role.
func setup(t *testing.T) env {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/users.sql")

	provider := authtest.NewProvider(t, testClientID)

	svc, err := auth.New(auth.Options{
		OIDC: &config.OIDC{
			Issuer:       provider.Issuer(),
			ClientID:     testClientID,
			ClientSecret: registry.SecretRef{Name: "sidekick-oidc", Key: "clientSecret"},
			GroupsClaim:  config.DefaultGroupsClaim,
			Roles: map[config.RoleName][]string{
				config.RoleAdmin:     {"sre-admins"},
				config.RoleResponder: {"engineering"},
			},
		},
		PublicURL: testPublicURL,
		Secrets:   staticSecrets{},
		Pool:      pool,
		Audit:     audit.NewWriter(),
		HTTP:      provider.Client(),
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	svc.Register(mux)

	return env{svc: svc, mux: mux, pool: pool, provider: provider}
}

// startLogin calls /auth/login and returns its answer.
func (e env) startLogin(t *testing.T, query string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, auth.LoginPath+query, nil))

	return rec
}

// callback calls /auth/callback with query and the cookies login set.
func (e env) callback(t *testing.T, query url.Values, login *httptest.ResponseRecorder) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, auth.CallbackPath+"?"+query.Encode(), nil)
	for _, c := range login.Result().Cookies() {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)

	return rec
}

// login signs in through the fake provider and returns the callback's
// answer.
func (e env) login(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()

	start := e.startLogin(t, "")
	require.Equal(t, http.StatusFound, start.Code)

	return e.callback(t, e.provider.Authorize(start.Header().Get("Location")), start)
}

// cookie returns the cookie name that rec set, or nil.
func cookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}

	return nil
}

// staticSecrets resolves every reference to the same fake client secret.
type staticSecrets struct{}

func (staticSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte("client-secret\n"), nil
}

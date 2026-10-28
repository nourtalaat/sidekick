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
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

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
	reSession        = sessionValue(0x04)
	roSession        = sessionValue(0x05)
)

const (
	viCSRF = "csrf-vi"
	adCSRF = "csrf-ad"
	reCSRF = "csrf-re"
	roCSRF = "csrf-ro"
)

const (
	// testListenRetry is how long a hub waits before it listens again.
	testListenRetry = 50 * time.Millisecond
	// testWait is how long a test waits for something that happens in the
	// background, and testPoll how often it looks.
	testWait = 10 * time.Second
	testPoll = 20 * time.Millisecond
)

// sourceYAML is a source section with credentials written in by mistake:
// a URL with a user and password, and a header value the email rule
// matches. GET /config must mask both.
//
//nolint:gosec // Fake credentials that GET /config must mask.
const sourceYAML = `
url: https://mcp-bot:hunter2@mcp.internal/mcp
headers:
  X-Contact: { value: pager@example.com }
`

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

	// What replica needs to make another app instance on the same
	// database.
	cfg      *config.Config
	redactor *redact.Redactor
	sessions *auth.Service
}

func setup(t *testing.T) server {
	t.Helper()

	return setupWith(t, nil)
}

// setupWith is setup with tune, when not nil, changing the API's options.
func setupWith(t *testing.T, tune func(*api.Options)) server {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/sessions.sql")
	storetest.LoadFixture(t, pool, "fixture/rules.sql")

	provider := authtest.NewProvider(t, "sidekick")

	var source yaml.Node

	require.NoError(t, yaml.Unmarshal([]byte(sourceYAML), &source))

	cfg := &config.Config{
		Server:   config.Server{PublicURL: "https://sidekick.example.com", Listen: ":8080"},
		Sources:  map[string]config.Section{"signoz": {Type: "mcp", Node: source.Content[0]}},
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

	s := server{provider: provider, pool: pool, cfg: cfg, redactor: redactor, sessions: sessions}

	return s.replica(t, tune)
}

// replica returns an app instance on the same database as s: its own
// handler, with its own hub and its own connection that listens, as
// another replica of sidekick serve has.
func (s server) replica(t *testing.T, tune func(*api.Options)) server {
	t.Helper()

	opts := api.Options{
		Auth: s.sessions, Tokens: s.sessions, Config: s.cfg, Rules: rules.NewStore(s.pool, s.cfg), Redactor: s.redactor,
		Pool: s.pool, Audit: audit.NewWriter(),
	}

	if tune != nil {
		tune(&opts)
	}

	if opts.Hub == nil {
		opts.Hub, _ = startHub(t, t.Context(), s.pool, api.HubOptions{ListenRetry: testListenRetry})
	}

	handler, err := api.New(opts)
	require.NoError(t, err)

	mux := http.NewServeMux()
	s.sessions.Register(mux)
	mux.Handle(api.BasePath+"/", handler)

	s.mux = mux

	return s
}

// startHub starts a hub that runs until ctx ends, waits until it listens,
// and returns it with the process ID of its connection.
func startHub(t *testing.T, ctx context.Context, pool *pgxpool.Pool, opts api.HubOptions) (*api.Hub, int) {
	t.Helper()

	before := listenerPIDs(t, pool)

	opts.Pool = pool
	hub := api.NewHub(opts)

	go func() { _ = hub.Run(ctx) }()

	var added []int

	require.Eventually(t, func() bool {
		added = slices.DeleteFunc(listenerPIDs(t, pool), func(pid int) bool { return slices.Contains(before, pid) })

		return len(added) == 1
	}, testWait, testPoll, "the hub never listened")

	return hub, added[0]
}

// listenerPIDs returns the process IDs of the connections of the test's
// database that listen for investigation updates, one for each hub that
// runs.
func listenerPIDs(t *testing.T, pool *pgxpool.Pool) []int {
	t.Helper()

	rows, err := pool.Query(t.Context(),
		`SELECT pid FROM pg_stat_activity
		 WHERE datname = current_database() AND pid <> pg_backend_pid() AND query LIKE '%LISTEN investigation_updates%'
		 ORDER BY pid`)
	require.NoError(t, err)

	pids, err := pgx.CollectRows(rows, pgx.RowTo[int])
	require.NoError(t, err)

	return pids
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

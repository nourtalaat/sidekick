//go:build integration

package teamsapp

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/teams"
)

// The identities of fixture/sidekick.yaml and the activity fixtures.
const (
	testAppID      = "00000000-1111-2222-3333-444444444444"
	testTenantID   = "55555555-6666-7777-8888-999999999999"
	testTeamID     = "6b6b6b6b-7777-8888-9999-000000000000"
	testServiceURL = "https://smba.trafficmanager.net/emea/"
	testKeyID      = "test-key"
	// testAckBudget replaces AckBudget in the tests.
	testAckBudget = time.Minute
	testRSABits   = 2048
)

// The paths of the fake Microsoft services, all on one fake server.
const (
	metadataPath = "/botframework/v1/.well-known/openidconfiguration"
	keysPath     = "/botframework/v1/.well-known/keys"
	loginPrefix  = "/login/"
	graphPrefix  = "/graph/v1.0/"
	tokenPath    = loginPrefix + testTenantID + "/oauth2/v2.0/token"
)

// signingKey is the Bot Framework key of the fake login service, made once
// for the test binary.
var signingKey = sync.OnceValue(func() *rsa.PrivateKey {
	key, err := rsa.GenerateKey(rand.Reader, testRSABits)
	if err != nil {
		panic(err)
	}

	return key
})

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Handler on a database of its own, with the config in
// fixture/sidekick.yaml and its calls to Microsoft sent to one fake server.
type harness struct {
	pool      *pgxpool.Pool
	handler   *Handler
	microsoft *fakehttp.Server
	reg       *prometheus.Registry
	spans     *tracetest.SpanRecorder
	// url serves the handler at MessagesPattern.
	url string

	mu sync.Mutex
	// keysDown makes the fake login service fail.
	keysDown bool
}

// setup loads the fixture config and builds a Handler on a new database,
// with a fake Microsoft that serves the Bot Framework signing keys. The
// test adds the token and Graph answers it expects.
func setup(t *testing.T) *harness {
	t.Helper()

	ctx := t.Context()

	cfg, err := config.Load(ctx, "fixture/sidekick.yaml", config.LoadOptions{Registries: config.Registries{Models: fakeModels()}})
	require.NoError(t, err)

	resolver := secrets.NewDir(cfg.Secrets.Dir)

	redactOpts, err := cfg.Redaction.RedactOptions(ctx, resolver)
	require.NoError(t, err)

	redactor, err := redact.New(redactOpts)
	require.NoError(t, err)

	h := &harness{microsoft: fakehttp.New(t), pool: storetest.New(t), reg: prometheus.NewRegistry(), spans: tracetest.NewSpanRecorder()}
	h.serveKeys(t)

	handler, err := New(Options{
		Config:         cfg,
		Secrets:        resolver,
		Redactor:       redactor,
		Pool:           h.pool,
		HTTP:           h.microsoft.Client(),
		Logger:         slog.New(slog.DiscardHandler),
		Metrics:        h.reg,
		TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(h.spans)),
		LoginURL:       h.microsoft.URL() + loginPrefix,
		MetadataURL:    h.microsoft.URL() + metadataPath,
		GraphURL:       h.microsoft.URL() + graphPrefix,
	})
	require.NoError(t, err)

	// A shared test database can be slower than the Bot Framework allows;
	// the tests check what is stored, not how fast.
	handler.ackBudget = testAckBudget
	h.handler = handler

	mux := http.NewServeMux()
	mux.Handle(MessagesPattern, handler)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	h.url = server.URL + "/teams/messages"

	return h
}

// serveKeys makes the fake login service publish the OpenID metadata and
// signingKey, endorsed for Teams.
func (h *harness) serveKeys(t *testing.T) {
	t.Helper()

	h.microsoft.HandleFunc(http.MethodGet, metadataPath, func(w http.ResponseWriter, _ *http.Request) {
		if h.down() {
			w.WriteHeader(http.StatusServiceUnavailable)

			return
		}

		writeJSON(t, w, map[string]any{"issuer": teams.BotFrameworkIssuer, "jwks_uri": h.microsoft.URL() + keysPath})
	})

	h.microsoft.HandleFunc(http.MethodGet, keysPath, func(w http.ResponseWriter, _ *http.Request) {
		key := signingKey()

		writeJSON(t, w, map[string]any{"keys": []map[string]any{{
			"kty":          "RSA",
			"use":          "sig",
			"kid":          testKeyID,
			"n":            base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e":            base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
			"endorsements": []string{teams.ChannelTeams},
		}}})
	})
}

// setKeysDown makes the fake login service fail, or work again.
func (h *harness) setKeysDown(down bool) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.keysDown = down
}

// down reports whether the fake login service fails.
func (h *harness) down() bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	return h.keysDown
}

// post sends body to the messaging endpoint with the Authorization header
// authorization, when it is not empty, and returns the status and body of
// the answer.
func (h *harness) post(t *testing.T, authorization string, body []byte) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.url, bytes.NewReader(body))
	require.NoError(t, err)

	req.Header.Set("Content-Type", "application/json")

	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(got)
}

// events returns every stored event, oldest first.
func (h *harness) events(t *testing.T) []db.Event {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT * FROM events ORDER BY id")
	require.NoError(t, err)

	events, err := pgx.CollectRows(rows, pgx.RowToStructByPos[db.Event])
	require.NoError(t, err)

	return events
}

// routeJobs counts the route_event jobs.
func (h *harness) routeJobs(t *testing.T) int {
	t.Helper()

	var n int
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM jobs WHERE kind = 'route_event'").Scan(&n))

	return n
}

// counter returns the value of the counter series of the metric name with
// the intake label intake and the label key set to value, or 0 when the
// series does not exist.
func (h *harness) counter(t *testing.T, name, intake, key, value string) float64 {
	t.Helper()

	families, err := h.reg.Gather()
	require.NoError(t, err)

	for _, f := range families {
		if f.GetName() != name {
			continue
		}

		for _, m := range f.GetMetric() {
			labels := map[string]string{}
			for _, l := range m.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}

			if labels["intake"] == intake && labels[key] == value {
				return m.GetCounter().GetValue()
			}
		}
	}

	return 0
}

// tokenClaims are the claims of a Bot Framework token for the test bot.
type tokenClaims struct {
	jwt.RegisteredClaims

	ServiceURL string `json:"serviceurl"`
}

// bearer returns an Authorization header with a Bot Framework token for
// the test bot and testServiceURL, signed with key, after change edits its
// claims.
func bearer(t *testing.T, key *rsa.PrivateKey, change func(c *tokenClaims)) string {
	t.Helper()

	now := time.Now()
	claims := tokenClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    teams.BotFrameworkIssuer,
			Audience:  jwt.ClaimStrings{testAppID},
			NotBefore: jwt.NewNumericDate(now.Add(-time.Minute)),
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Hour)),
		},
		ServiceURL: testServiceURL,
	}

	if change != nil {
		change(&claims)
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = testKeyID

	signed, err := token.SignedString(key)
	require.NoError(t, err)

	return "Bearer " + signed
}

// readFixture returns the content of a file under fixture/.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	content, err := fs.ReadFile(os.DirFS("fixture"), name)
	require.NoError(t, err)

	return content
}

// writeJSON answers with v as JSON.
func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")

	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encode the answer: %v", err)
	}
}

// fakeModels is a model registry with a provider named fake, so the
// fixture config loads.
func fakeModels() *registry.Registry[model.Connector] {
	models := registry.New[model.Connector]("model")
	models.Register("fake", registry.Plugin[model.Connector]{
		Build: func(context.Context, string, registry.Decoder, registry.Deps) (model.Connector, error) {
			return nil, nil
		},
	})

	return models
}

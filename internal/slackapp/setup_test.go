//go:build integration

package slackapp

import (
	"bytes"
	"context"
	"encoding/hex"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"
	"time"

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
)

// The test values of the secrets in fixture/secrets.
const (
	testBotToken      = "xoxb-test-bot-token" //nolint:gosec // A test value, as in fixture/secrets.
	testSigningSecret = "test-signing-secret"
	// testAckBudget replaces AckBudget in the tests.
	testAckBudget = time.Minute
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Handler on a database of its own, with the config in
// fixture/sidekick.yaml and its Web API calls sent to a fake Slack.
type harness struct {
	cfg     *config.Config
	pool    *pgxpool.Pool
	handler *Handler
	slack   *fakehttp.Server
	reg     *prometheus.Registry
	spans   *tracetest.SpanRecorder
	// url serves the handler at EventsPattern.
	url string
}

// setup loads the fixture config and builds a Handler on a new database,
// with a fake Slack Web API the test adds its responses to.
func setup(t *testing.T) *harness {
	t.Helper()

	ctx := t.Context()

	cfg, err := config.Load(ctx, "fixture/sidekick.yaml", config.LoadOptions{Registries: config.Registries{Models: fakeModels()}})
	require.NoError(t, err)

	slack := fakehttp.New(t)
	cfg.Slack.APIURL = slack.URL() + "/"

	resolver := secrets.NewDir(cfg.Secrets.Dir)

	redactOpts, err := cfg.Redaction.RedactOptions(ctx, resolver)
	require.NoError(t, err)

	redactor, err := redact.New(redactOpts)
	require.NoError(t, err)

	pool := storetest.New(t)
	reg := prometheus.NewRegistry()
	spans := tracetest.NewSpanRecorder()

	h, err := New(Options{
		Config:         cfg,
		Secrets:        resolver,
		Redactor:       redactor,
		Pool:           pool,
		HTTP:           slack.Client(),
		Logger:         slog.New(slog.DiscardHandler),
		Metrics:        reg,
		TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)),
	})
	require.NoError(t, err)

	// A shared test database can be slower than Slack allows; the tests
	// check what is stored, not how fast.
	h.ackBudget = testAckBudget

	mux := http.NewServeMux()
	mux.Handle(EventsPattern, h)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &harness{cfg: cfg, pool: pool, handler: h, slack: slack, reg: reg, spans: spans, url: server.URL + "/slack/events"}
}

// post sends body to the Events API with the given headers and returns the
// status and the body of the answer.
func (h *harness) post(t *testing.T, header http.Header, body []byte) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.url, bytes.NewReader(body))
	require.NoError(t, err)

	for name, values := range header {
		req.Header[name] = values
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

// signed returns the headers that sign body with secret at the time at.
func signed(secret string, at time.Time, body []byte) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)

	return http.Header{
		headerTimestamp: {ts},
		headerSignature: {signatureScheme + "=" + hex.EncodeToString(sign([]byte(secret), ts, body))},
	}
}

// readFixture returns the content of a file under fixture/.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	content, err := fs.ReadFile(os.DirFS("fixture"), name)
	require.NoError(t, err)

	return content
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

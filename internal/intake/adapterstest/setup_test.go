//go:build integration

package adapterstest

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/intake"
	_ "github.com/nourtalaat/sidekick/internal/intake/datadog" // Adapter "datadog".
	_ "github.com/nourtalaat/sidekick/internal/intake/grafana" // Adapter "grafana".
	"github.com/nourtalaat/sidekick/internal/intake/ingest"
	"github.com/nourtalaat/sidekick/internal/intake/sentry"
	_ "github.com/nourtalaat/sidekick/internal/intake/signoz" // Adapter "signoz".
	"github.com/nourtalaat/sidekick/internal/intake/webhook"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The test values of the secrets in fixture/secrets.
//
//nolint:gosec // Test values, not real credentials.
const (
	signozToken       = "signoz-test-token"
	datadogToken      = "datadog-test-token"
	sentrySecret      = "sentry-test-client-secret"
	grafanaToken      = "grafana-test-token"
	grafanaHMACSecret = "grafana-test-hmac-secret"
)

// grafanaTimestampHeader is the timestamp header of the grafana-signed
// endpoint in fixture/sidekick.yaml.
const grafanaTimestampHeader = "X-Grafana-Alerting-Timestamp"

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a webhook handler with the endpoints of fixture/sidekick.yaml,
// on a database of its own, served over HTTP.
type harness struct {
	url      string
	pool     *pgxpool.Pool
	adapters map[string]intake.Adapter
}

// delivery is one request to send to the handler.
type delivery struct {
	endpoint string
	header   http.Header
	body     []byte
}

// setup loads the fixture config, builds its adapters and redactor, and
// serves a webhook handler on a new database.
func setup(t *testing.T) *harness {
	t.Helper()

	ctx := t.Context()

	cfg, err := config.Load(ctx, "fixture/sidekick.yaml", config.LoadOptions{Registries: config.Registries{Models: fakeModels()}})
	require.NoError(t, err)

	resolver := secrets.NewDir(cfg.Secrets.Dir)
	adapters := map[string]intake.Adapter{}

	for name, w := range cfg.Intake.Webhooks {
		adapters[name], err = registry.Intakes.Build(ctx, w.Type, name, w.Section, registry.Deps{Secrets: resolver})
		require.NoError(t, err)
	}

	redactOpts, err := cfg.Redaction.RedactOptions(ctx, resolver)
	require.NoError(t, err)

	redactor, err := redact.New(redactOpts)
	require.NoError(t, err)

	metrics, err := ingest.NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	pool := storetest.New(t)

	h, err := webhook.New(webhook.Options{
		Webhooks: cfg.Intake.Webhooks,
		Adapters: adapters,
		Secrets:  resolver,
		Redactor: redactor,
		Pool:     pool,
		Logger:   slog.New(slog.DiscardHandler),
		Metrics:  metrics,
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(webhook.Pattern, h)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &harness{url: server.URL, pool: pool, adapters: adapters}
}

// send posts d and returns the status and the body of the answer.
func (h *harness) send(t *testing.T, d delivery) (int, []byte) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.url+"/hooks/"+d.endpoint, bytes.NewReader(d.body))
	require.NoError(t, err)

	for name, values := range d.header {
		req.Header[name] = values
	}

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, body
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

// bearer is a delivery to endpoint with an Authorization: Bearer token.
func bearer(endpoint, token string, body []byte) delivery {
	return delivery{endpoint: endpoint, header: http.Header{"Authorization": {"Bearer " + token}}, body: body}
}

// sentrySigned is a delivery to the sentry endpoint signed as Sentry signs
// it, with key.
func sentrySigned(key string, body []byte) delivery {
	return delivery{
		endpoint: "sentry",
		header:   http.Header{sentry.HeaderSignature: {hex.EncodeToString(sentry.Sign([]byte(key), body))}},
		body:     body,
	}
}

// grafanaSigned is a delivery to the grafana-signed endpoint signed as
// Grafana signs it with a timestamp header, with key at the time at.
func grafanaSigned(key string, at time.Time, body []byte) delivery {
	ts := strconv.FormatInt(at.Unix(), 10)

	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(ts + ":"))
	mac.Write(body)

	return delivery{
		endpoint: "grafana-signed",
		header: http.Header{
			"X-Grafana-Alerting-Signature": {hex.EncodeToString(mac.Sum(nil))},
			grafanaTimestampHeader:         {ts},
		},
		body: body,
	}
}

// readPayload returns a recorded payload, by its path from internal/intake.
func readPayload(t *testing.T, name string) []byte {
	t.Helper()

	content, err := fs.ReadFile(os.DirFS(".."), name)
	require.NoError(t, err)

	return content
}

// readFixture returns the content of a file under this package's fixture/.
func readFixture(t *testing.T, name string) []byte {
	t.Helper()

	content, err := fs.ReadFile(os.DirFS("fixture"), name)
	require.NoError(t, err)

	return content
}

// requireResponse checks that body is the JSON in the response fixture
// file, with each event ID replaced by "ID".
func requireResponse(t *testing.T, file string, body []byte) {
	t.Helper()

	var got map[string]any
	require.NoError(t, json.Unmarshal(body, &got), "the response is not JSON: %s", body)

	if events, ok := got["events"].([]any); ok {
		for _, e := range events {
			entry, ok := e.(map[string]any)
			require.True(t, ok)

			if _, ok := entry["id"]; ok {
				entry["id"] = "ID"
			}
		}
	}

	var want map[string]any
	require.NoError(t, json.Unmarshal(readFixture(t, "responses/"+file), &want))
	require.Equal(t, want, got)
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

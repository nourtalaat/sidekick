//go:build integration

package webhook

import (
	"bytes"
	"context"
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
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/intake"
	_ "github.com/nourtalaat/sidekick/internal/intake/alertmanager" // Adapter "alertmanager".
	_ "github.com/nourtalaat/sidekick/internal/intake/generic"      // Adapter "generic".
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The test values of the secrets in fixture/secrets.
const (
	testBearerToken = "am-test-token"
	testHMACKey     = "ci-test-hmac-key"
)

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a webhook handler on a database of its own, served over HTTP
// with the config in fixture/sidekick.yaml.
type harness struct {
	url  string
	pool *pgxpool.Pool
}

// delivery is one request to send to the handler.
type delivery struct {
	endpoint string
	header   http.Header
	body     []byte
}

// jobRow is a job as the tests check it.
type jobRow struct {
	Kind      string
	EventID   string
	UniqueKey *string
}

// setup loads the fixture config, builds the adapters and the redactor
// from it, and serves a Handler on a new database seeded with the given
// fixture files.
func setup(t *testing.T, seeds ...string) *harness {
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

	pool := storetest.New(t)
	for _, seed := range seeds {
		storetest.LoadFixture(t, pool, seed)
	}

	h, err := New(Options{
		Webhooks: cfg.Intake.Webhooks,
		Adapters: adapters,
		Secrets:  resolver,
		Redactor: redactor,
		Pool:     pool,
		Logger:   slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	mux := http.NewServeMux()
	mux.Handle(Pattern, h)

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	return &harness{url: server.URL, pool: pool}
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

// jobs returns every job, oldest first.
func (h *harness) jobs(t *testing.T) []jobRow {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT kind, payload->>'event_id', unique_key FROM jobs ORDER BY id")
	require.NoError(t, err)

	jobs, err := pgx.CollectRows(rows, pgx.RowToStructByPos[jobRow])
	require.NoError(t, err)

	return jobs
}

// eventsAsText returns every stored event, every column, as JSON text.
func (h *harness) eventsAsText(t *testing.T) string {
	t.Helper()

	var text string
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT COALESCE(json_agg(e)::text, '') FROM events e").Scan(&text))

	return text
}

// bearer is a delivery to endpoint with an Authorization: Bearer token.
func bearer(endpoint, token string, body []byte) delivery {
	return delivery{endpoint: endpoint, header: http.Header{"Authorization": {"Bearer " + token}}, body: body}
}

// signed is a delivery to endpoint signed with key at the time at.
func signed(endpoint, key string, at time.Time, body []byte) delivery {
	ts := strconv.FormatInt(at.Unix(), 10)

	return delivery{
		endpoint: endpoint,
		header: http.Header{
			intake.HeaderTimestamp: {ts},
			intake.HeaderSignature: {intake.SignatureHeader([]byte(key), ts, body)},
		},
		body: body,
	}
}

// readFixture returns the content of a file under fixture/.
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

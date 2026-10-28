//go:build integration

package slackcmd_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/rules"
	"github.com/nourtalaat/sidekick/internal/secrets"
	"github.com/nourtalaat/sidekick/internal/slackapp"
	"github.com/nourtalaat/sidekick/internal/slackcmd"
	"github.com/nourtalaat/sidekick/internal/slackroles"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The Slack users of the tests: U01ADMIN and U01VIEWER have their roles
// from slack.roleUsers in fixture/sidekick.yaml, and U01ANA is in neither
// map, so she is a responder.
const (
	admin     = "U01ADMIN"
	viewer    = "U01VIEWER"
	responder = "U01ANA"
)

// appID is the ID of the Sidekick app in the tests.
const appID = "A01SIDEKICK"

// testTimeout replaces the command timeout in the tests.
const testTimeout = time.Minute

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness serves POST /slack/commands on a database of its own, seeded
// with fixture/rules.sql, with Slack Web API calls sent to a fake Slack.
type harness struct {
	pool    *pgxpool.Pool
	slack   *fakehttp.Server
	cache   *rules.Cache
	url     string
	secret  []byte
	trigger atomic.Int64
}

func setup(t *testing.T) *harness {
	t.Helper()

	ctx := t.Context()

	cfg, err := config.Load(ctx, "fixture/sidekick.yaml", config.LoadOptions{Registries: config.Registries{Models: fakeModels()}})
	require.NoError(t, err)

	fake := fakehttp.New(t)
	cfg.Slack.APIURL = fake.URL() + "/"

	resolver := secrets.NewDir(cfg.Secrets.Dir)

	redactOpts, err := cfg.Redaction.RedactOptions(ctx, resolver)
	require.NoError(t, err)

	redactor, err := redact.New(redactOpts)
	require.NoError(t, err)

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/rules.sql")

	log := slog.New(slog.DiscardHandler)

	app, err := slackapp.New(slackapp.Options{
		Config: cfg, Secrets: resolver, Redactor: redactor, Pool: pool, HTTP: fake.Client(), Logger: log, Metrics: prometheus.NewRegistry(),
	})
	require.NoError(t, err)

	cache := rules.NewCache(ctx, rules.Options{File: cfg, Pool: pool})
	require.NoError(t, cache.Reload(ctx))

	lookup, err := slackroles.New(slackroles.Options{Slack: cfg.Slack, Secrets: resolver, Pool: pool, HTTP: fake.Client()})
	require.NoError(t, err)

	commands := slackcmd.New(slackcmd.Options{
		File: cfg, Store: rules.NewStore(pool, cfg), Rules: cache, Roles: lookup, Investigator: app, Logger: log, Version: "v0.0.0-test",
		// A shared test database can be slower than Slack allows; the
		// tests check the replies, not how fast they come.
		Timeout: testTimeout,
	})

	mux := http.NewServeMux()
	mux.Handle(slackcmd.CommandsPattern, slackcmd.NewHTTPHandler(commands, app))

	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)

	secret, err := resolver.Resolve(ctx, cfg.Slack.SigningSecret)
	require.NoError(t, err)

	return &harness{pool: pool, slack: fake, cache: cache, url: server.URL + "/slack/commands", secret: []byte(strings.TrimSpace(string(secret)))}
}

// command sends /sidekick text as user, signed, and returns the status and
// the reply's text.
func (h *harness) command(t *testing.T, user, text string) (int, string) {
	t.Helper()

	body := h.form(user, text)

	status, raw := h.post(t, h.sign(time.Now(), body), body)
	if status != http.StatusOK {
		return status, raw
	}

	var reply struct {
		ResponseType string `json:"response_type"`
		Text         string `json:"text"`
	}

	require.NoError(t, json.Unmarshal([]byte(raw), &reply))
	require.Equal(t, "ephemeral", reply.ResponseType)

	return status, reply.Text
}

// form encodes a slash command as Slack posts it, with a new trigger ID.
func (h *harness) form(user, text string) []byte {
	return []byte(url.Values{
		"team_id":    {"T01TEAM"},
		"channel_id": {"C01OPS"},
		"user_id":    {user},
		"command":    {"/sidekick"},
		"text":       {text},
		"trigger_id": {"trigger-" + strconv.FormatInt(h.trigger.Add(1), 10)},
		"api_app_id": {appID},
	}.Encode())
}

// sign returns the headers that sign body with the signing secret at at.
func (h *harness) sign(at time.Time, body []byte) http.Header {
	ts := strconv.FormatInt(at.Unix(), 10)

	mac := hmac.New(sha256.New, h.secret)
	mac.Write([]byte("v0:" + ts + ":"))
	mac.Write(body)

	return http.Header{
		"Content-Type":              {"application/x-www-form-urlencoded"},
		"X-Slack-Request-Timestamp": {ts},
		"X-Slack-Signature":         {"v0=" + hex.EncodeToString(mac.Sum(nil))},
	}
}

func (h *harness) post(t *testing.T, header http.Header, body []byte) (int, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, h.url, strings.NewReader(string(body)))
	require.NoError(t, err)

	req.Header = header

	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)

	defer func() { require.NoError(t, resp.Body.Close()) }()

	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp.StatusCode, string(got)
}

// auditActions returns the action of every audit row, oldest first.
func (h *harness) auditActions(t *testing.T) []string {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), "SELECT action || ' ' || target_kind || ':' || target_id || ' by ' || actor_id FROM audit_log ORDER BY id")
	require.NoError(t, err)

	defer rows.Close()

	var out []string

	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		out = append(out, s)
	}

	require.NoError(t, rows.Err())

	return out
}

// mentionEvents counts the stored events from mentions and
// /sidekick investigate.
func (h *harness) mentionEvents(t *testing.T) int {
	t.Helper()

	var n int
	require.NoError(t, h.pool.QueryRow(t.Context(), "SELECT count(*) FROM events WHERE intake = 'slack-mention' AND kind = 'mention'").Scan(&n))

	return n
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

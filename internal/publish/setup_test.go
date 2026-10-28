//go:build integration

package publish

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/nourtalaat/sidekick/internal/fakehttp"
	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/output/slack"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/registry"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The investigations in fixture/investigations.sql.
var (
	// webhookInvestigation is about a webhook alert, so its message goes
	// in the Slack output's channel.
	webhookInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")
	// slackInvestigation is about an alert posted in Slack, in a group of
	// two events, so its message goes in that alert's thread.
	slackInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c2")
)

// slackSink is the name of the Slack output in the tests.
const slackSink = "slack-thread"

// testWorker is the locked_by value of jobs the tests claim by hand.
const testWorker = "test-worker"

// testLease is the lease of jobs the tests claim by hand.
const testLease = time.Minute

// waitTimeout bounds how long a test waits for the runner.
const waitTimeout = 30 * time.Second

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a publish Handler with a real Slack output that talks to a
// fake Slack, on a database of its own seeded with
// fixture/investigations.sql.
type harness struct {
	pool    *pgxpool.Pool
	slack   *fakeSlack
	handler *Handler
	spans   *tracetest.SpanRecorder
}

func setup(t *testing.T) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigations.sql")

	fake := newFakeSlack(t)

	sink, err := slack.New(t.Context(), slackSink, slack.Config{Channel: "#alerts", APIURL: fake.api.URL() + "/api/"}, registry.Deps{
		Secrets:       staticSecrets{},
		HTTP:          fake.api.Client(),
		SlackBotToken: registry.SecretRef{Name: "sidekick-slack", Key: "botToken"},
	})
	require.NoError(t, err)

	return setupWith(t, pool, fake, map[string]output.Sink{slackSink: sink})
}

// setupWith returns a harness for sinks on pool.
func setupWith(t *testing.T, pool *pgxpool.Pool, fake *fakeSlack, sinks map[string]output.Sink) *harness {
	t.Helper()

	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	return &harness{
		pool:    pool,
		slack:   fake,
		handler: New(Options{Pool: pool, Sinks: sinks, TracerProvider: tp}),
		spans:   spans,
	}
}

// open calls Open for id in its own transaction.
func (h *harness) open(t *testing.T, id uuid.UUID, sinks ...string) {
	t.Helper()

	h.inTx(t, func(tx pgx.Tx) error { return Open(t.Context(), tx, id, sinks) })
}

// enqueue calls Enqueue with u in its own transaction.
func (h *harness) enqueue(t *testing.T, u Update) {
	t.Helper()

	h.inTx(t, func(tx pgx.Tx) error { return Enqueue(t.Context(), tx, u) })
}

func (h *harness) inTx(t *testing.T, f func(tx pgx.Tx) error) {
	t.Helper()

	tx, err := h.pool.Begin(t.Context())
	require.NoError(t, err)
	require.NoError(t, f(tx))
	require.NoError(t, tx.Commit(t.Context()))
}

// claim claims the ready publish job, as a worker does.
func (h *harness) claim(t *testing.T) db.Job {
	t.Helper()

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: testWorker, LeaseSeconds: testLease.Seconds(), Kinds: []string{queue.KindPublish}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1, "one publish job is ready")

	return jobs[0]
}

// run claims the ready publish job, runs it and completes it, as the queue
// runner does, and returns the handler's error.
func (h *harness) run(t *testing.T) error {
	t.Helper()

	job := h.claim(t)

	if err := h.handler.Handle(t.Context(), job); err != nil {
		return err
	}

	_, err := db.New(h.pool).CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
	require.NoError(t, err)

	return nil
}

// jobs returns the publish jobs, oldest first.
func (h *harness) jobs(t *testing.T) []publishJob {
	t.Helper()

	return collect[publishJob](t, h.pool, "SELECT status, unique_key, payload, max_attempts FROM jobs WHERE kind = 'publish' ORDER BY id")
}

// outputRef returns the stored ref of id in sink.
func (h *harness) outputRef(t *testing.T, id uuid.UUID, sink string) db.OutputRef {
	t.Helper()

	refs := collect[db.OutputRef](t, h.pool, fmt.Sprintf(
		"SELECT * FROM output_refs WHERE investigation_id = '%s' AND sink = '%s'", id, sink))
	require.Len(t, refs, 1)

	return refs[0]
}

// state returns the publish state of id.
func (h *harness) state(t *testing.T, id uuid.UUID) db.PublishState {
	t.Helper()

	states := collect[db.PublishState](t, h.pool, fmt.Sprintf("SELECT * FROM publish_states WHERE investigation_id = '%s'", id))
	require.Len(t, states, 1)

	return states[0]
}

// publishJob is a publish job as the tests check it.
type publishJob struct {
	Status      string
	UniqueKey   *string
	Payload     json.RawMessage
	MaxAttempts int32
}

// collect runs query and returns its rows as T, column by column.
func collect[T any](t *testing.T, pool *pgxpool.Pool, query string) []T {
	t.Helper()

	rows, err := pool.Query(t.Context(), query)
	require.NoError(t, err)

	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[T])
	require.NoError(t, err)

	return out
}

// staticSecrets resolves every reference to a fake bot token.
type staticSecrets struct{}

func (staticSecrets) Resolve(context.Context, registry.SecretRef) ([]byte, error) {
	return []byte("fake-value\n"), nil
}

// slackCall is one call the fake Slack got.
type slackCall struct {
	Method   string
	Channel  string `json:"channel"`
	TS       string `json:"ts"`
	ThreadTS string `json:"thread_ts"`
	Text     string `json:"text"`
	// Attachments are kept as sent, for the tests that read the blocks.
	Attachments json.RawMessage `json:"attachments"`
}

// fakeSlack is the Slack Web API for one test. Each post gets the next
// timestamp, and a test can hold an edit until it lets it go.
type fakeSlack struct {
	api *fakehttp.Server

	mu    sync.Mutex
	posts int
	calls []slackCall
	// hold, when set, is closed by the test to let the next chat.update
	// answer; held is closed when that call arrives.
	hold, held chan struct{}
}

func newFakeSlack(t *testing.T) *fakeSlack {
	t.Helper()

	f := &fakeSlack{api: fakehttp.New(t)}

	f.api.HandleFunc(http.MethodPost, "/api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		call := f.record(t, "chat.postMessage", r)

		f.mu.Lock()
		f.posts++
		ts := fmt.Sprintf("1793023321.%06d", f.posts)
		f.mu.Unlock()

		answer(w, `{"ok":true,"channel":"%s","ts":"%s"}`, channelID(call.Channel), ts)
	})

	f.api.HandleFunc(http.MethodPost, "/api/chat.update", func(w http.ResponseWriter, r *http.Request) {
		call := f.record(t, "chat.update", r)

		f.mu.Lock()
		hold, held := f.hold, f.held
		f.hold, f.held = nil, nil
		f.mu.Unlock()

		if hold != nil {
			close(held)
			<-hold
		}

		answer(w, `{"ok":true,"channel":"%s","ts":"%s"}`, call.Channel, call.TS)
	})

	f.api.HandleFunc(http.MethodGet, "/api/chat.getPermalink", func(w http.ResponseWriter, r *http.Request) {
		answer(w, `{"ok":true,"permalink":"https://example.slack.com/archives/%s/p%s"}`, r.URL.Query().Get("channel"), r.URL.Query().Get("message_ts"))
	})

	return f
}

// holdNextEdit makes the next chat.update wait. held is closed when the
// call arrives; closing release lets it answer.
func (f *fakeSlack) holdNextEdit() (held <-chan struct{}, release chan<- struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.hold, f.held = make(chan struct{}), make(chan struct{})

	return f.held, f.hold
}

// record decodes and keeps one call.
func (f *fakeSlack) record(t *testing.T, method string, r *http.Request) slackCall {
	t.Helper()

	call := slackCall{Method: method}
	if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
		t.Errorf("decode the %s body: %v", method, err)
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls = append(f.calls, call)

	return call
}

// callsTo returns the calls to method, in order.
func (f *fakeSlack) callsTo(method string) []slackCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []slackCall

	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}

	return out
}

// channelID is the ID Slack answers with for a channel name.
func channelID(channel string) string {
	if channel == "#alerts" {
		return "C123"
	}

	return channel
}

func answer(w http.ResponseWriter, format string, args ...any) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, format, args...) //nolint:gosec // A fake API answering the code under test, not a browser.
}

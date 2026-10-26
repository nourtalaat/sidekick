//go:build integration

package router

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/nourtalaat/sidekick/internal/config"
	"github.com/nourtalaat/sidekick/internal/grouper"
	"github.com/nourtalaat/sidekick/internal/intake/webhook"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
)

// The IDs of the events in fixture/events.sql.
var (
	paymentsEvent = uuid.MustParse("0192d3a4-0000-7000-8000-000000000001")
	diskEvent     = uuid.MustParse("0192d3a4-0000-7000-8000-000000000002")
	diskAgain     = uuid.MustParse("0192d3a4-0000-7000-8000-000000000003")
	resolvedEvent = uuid.MustParse("0192d3a4-0000-7000-8000-000000000004")
	otherEvent    = uuid.MustParse("0192d3a4-0000-7000-8000-000000000005")
)

// fixtureFingerprints is how many fingerprints the events in
// fixture/events.sql have.
const fixtureFingerprints = 3

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// harness is a Router with the routes of fixture/sidekick.yaml on a
// database of its own, seeded with fixture/events.sql, and the webhook
// handler of the config's endpoints in front of it.
type harness struct {
	cfg    *config.Config
	pool   *pgxpool.Pool
	router *Router
	hooks  *webhook.Handler
	spans  *tracetest.SpanRecorder
	// intake makes the spans that enqueue route_event jobs, as the
	// intake.receive span does.
	intake trace.Tracer
}

// investigateJob is an investigate job as the tests check it.
type investigateJob struct {
	Route       *string
	UniqueKey   *string
	Payload     json.RawMessage
	Traceparent *string
}

func setup(t *testing.T) *harness {
	t.Helper()

	cfg, instances := loadConfig(t)

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/events.sql")

	spans := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans))

	r, err := New(Options{
		Config:         cfg,
		ModelSettings:  instances.ModelSettings,
		Pool:           pool,
		Logger:         slog.New(slog.DiscardHandler),
		TracerProvider: tp,
	})
	require.NoError(t, err)

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	hooks, err := webhook.New(webhook.Options{
		Webhooks: cfg.Intake.Webhooks,
		Adapters: instances.Intakes,
		Secrets:  fakeSecrets{},
		Redactor: redactor,
		Pool:     pool,
		Logger:   slog.New(slog.DiscardHandler),
	})
	require.NoError(t, err)

	return &harness{cfg: cfg, pool: pool, router: r, hooks: hooks, spans: spans, intake: tp.Tracer("intake")}
}

// deliver posts the Alertmanager payload in file to the alertmanager
// endpoint, as Alertmanager does.
func (h *harness) deliver(t *testing.T, file string) {
	t.Helper()

	body, err := os.ReadFile(file) //nolint:gosec // A fixture path chosen by the test.
	require.NoError(t, err)

	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hooks/alertmanager", bytes.NewReader(body))
	req.SetPathValue("name", "alertmanager")
	req.Header.Set("Authorization", "Bearer fake-value")
	req.Header.Set("Content-Type", "application/json")

	rec := httptest.NewRecorder()
	h.hooks.ServeHTTP(rec, req)
	require.Equal(t, http.StatusAccepted, rec.Code, rec.Body.String())
}

// routeAll claims every ready route_event job and runs it, as the workers
// do, until none is left.
func (h *harness) routeAll(t *testing.T) {
	t.Helper()

	for {
		jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
			Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindRouteEvent}, Batch: 1,
		})
		require.NoError(t, err)

		if len(jobs) == 0 {
			return
		}

		require.NoError(t, h.router.Handle(t.Context(), jobs[0]))

		_, err = db.New(h.pool).CompleteJob(t.Context(), db.CompleteJobParams{ID: jobs[0].ID, ClaimToken: *jobs[0].ClaimToken})
		require.NoError(t, err)
	}
}

// closeGroups runs every pending close_group job at once, with a Closer
// whose clock says now.
func (h *harness) closeGroups(t *testing.T, now time.Time) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET run_at = now() WHERE kind = 'close_group' AND status = 'pending'")
	require.NoError(t, err)

	closer := grouper.NewCloser(grouper.CloserOptions{Pool: h.pool, Now: func() time.Time { return now }})

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindCloseGroup}, Batch: 100,
	})
	require.NoError(t, err)

	for _, job := range jobs {
		require.NoError(t, closer.Handle(t.Context(), job))

		_, err = db.New(h.pool).CompleteJob(t.Context(), db.CompleteJobParams{ID: job.ID, ClaimToken: *job.ClaimToken})
		require.NoError(t, err)
	}
}

// inbox returns the inbox items of every investigation, oldest first.
func (h *harness) inbox(t *testing.T) []db.InvestigationInbox {
	t.Helper()

	return collect[db.InvestigationInbox](t, h.pool, "SELECT * FROM investigation_inbox ORDER BY id")
}

// routeJob enqueues the route_event job of the event with id inside an
// intake span, as intake does, and returns the job as a worker claims it.
func (h *harness) routeJob(t *testing.T, id uuid.UUID) db.Job {
	t.Helper()

	ctx, span := h.intake.Start(t.Context(), "intake.receive")
	defer span.End()

	payload, err := json.Marshal(routeEventPayload{EventID: id})
	require.NoError(t, err)

	return h.enqueue(t, ctx, payload)
}

// enqueue adds a route_event job with payload and returns it.
func (h *harness) enqueue(t *testing.T, ctx context.Context, payload []byte) db.Job {
	t.Helper()

	jobID, err := queue.Enqueue(ctx, h.pool, queue.NewJob{Kind: queue.KindRouteEvent, Payload: payload})
	require.NoError(t, err)

	job, err := db.New(h.pool).GetJob(t.Context(), jobID)
	require.NoError(t, err)

	return job
}

// event returns the stored event with id.
func (h *harness) event(t *testing.T, id uuid.UUID) db.Event {
	t.Helper()

	ev, err := db.New(h.pool).GetEvent(t.Context(), id)
	require.NoError(t, err)

	return ev
}

// groups returns every event group, oldest first.
func (h *harness) groups(t *testing.T) []db.EventGroup {
	t.Helper()

	return collect[db.EventGroup](t, h.pool, "SELECT * FROM event_groups ORDER BY id")
}

// investigations returns every investigation, oldest first.
func (h *harness) investigations(t *testing.T) []db.Investigation {
	t.Helper()

	return collect[db.Investigation](t, h.pool, "SELECT * FROM investigations ORDER BY id")
}

// investigateJobs returns every investigate job, oldest first.
func (h *harness) investigateJobs(t *testing.T) []investigateJob {
	t.Helper()

	return collect[investigateJob](t, h.pool, "SELECT route, unique_key, payload, traceparent FROM jobs WHERE kind = 'investigate' ORDER BY id")
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

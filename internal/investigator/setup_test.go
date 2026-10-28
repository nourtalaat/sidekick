//go:build integration

package investigator

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/store/storetest"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/strategies"
)

// testInvestigation is the investigation in fixture/investigation.sql.
var testInvestigation = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000c1")

// testModel is the model the fixture's investigation records, which the
// shipped price table has.
const testModel = "claude-opus-5-5"

// stoppedAt is when the clock stands still for options.stoppedTime.
var stoppedAt = time.Date(2026, 10, 26, 14, 2, 5, 0, time.UTC)

// testTriageConnector names the connector of the triage role, when a test
// sets one.
const testTriageConnector = "cheap"

// testWorker is the locked_by value of jobs the tests claim by hand.
const testWorker = "test-worker"

// testLease is the lease of jobs the tests claim by hand.
const testLease = time.Minute

// testUsage is the token usage of every scripted model turn.
var testUsage = model.Usage{InputTokens: 1000, OutputTokens: 200, CacheReadTokens: 5000, CacheWriteTokens: 300}

// signozDashboard is the evidence link the fake source returns.
var signozDashboard = source.Evidence{Kind: source.EvidenceMetric, Title: "SigNoz", URL: "https://signoz.example.com/d/1"}

// TestMain runs the tests, then lets storetest stop the Postgres container
// it may have started.
func TestMain(m *testing.M) {
	os.Exit(storetest.Main(m))
}

// options change what setup builds.
type options struct {
	turns  []modeltest.Turn
	budget Budget
	// caps are the connector's capabilities; tool calling is always on.
	caps model.Capabilities
	// startedAgo, when set, makes the investigation look as if its first
	// run started that long ago.
	startedAgo time.Duration
	// stoppedTime stops the handler's clock at stoppedAt, when the run
	// starts, so the final turn for the wall time never starts, and the
	// wall time is counted only from the start of the agent loop, after
	// the prompt is saved.
	stoppedTime bool
	// route is the payments route's settings, and daily the daily caps.
	route Route
	daily DailyCaps
	// source replaces the fake signoz source when it is set.
	source source.Source
	// triageTurns, when set, give the investigation a triage role filled
	// by a scripted connector of its own, cheap, that answers with them.
	triageTurns []modeltest.Turn
}

// harness is a Handler with a scripted connector and a fake signoz source
// behind a real guard, on a database of its own seeded with
// fixture/investigation.sql.
type harness struct {
	pool     *pgxpool.Pool
	handler  *Handler
	conn     *modeltest.Connector
	spans    *tracetest.SpanRecorder
	registry *prometheus.Registry
	// triage is the triage connector, when options.triageTurns are set.
	triage *modeltest.Connector
}

func setup(t *testing.T, o options) *harness {
	t.Helper()

	pool := storetest.New(t)
	storetest.LoadFixture(t, pool, "fixture/investigation.sql")

	if o.budget == (Budget{}) {
		o.budget = Budget{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute}
	}

	budget, err := json.Marshal(o.budget)
	require.NoError(t, err)

	_, err = pool.Exec(t.Context(), "UPDATE investigations SET budget = $1 WHERE id = $2", budget, testInvestigation)
	require.NoError(t, err)

	if o.startedAgo > 0 {
		_, err = pool.Exec(t.Context(), "UPDATE investigations SET started_at = now() - make_interval(secs => $1) WHERE id = $2",
			o.startedAgo.Seconds(), testInvestigation)
		require.NoError(t, err)
	}

	o.caps.ToolCalling = true
	conn := modeltest.New(t, o.caps, o.turns...)
	conn.ExpectAllUsed()

	src := o.source
	if src == nil {
		src = fakeSignoz(t)
	}

	h := &harness{pool: pool, conn: conn, spans: tracetest.NewSpanRecorder(), registry: prometheus.NewRegistry()}
	h.handler = newHandler(t, pool, conn, src, h.spans, h.registry)
	h.handler.opts.Routes["payments"] = o.route
	h.handler.opts.Daily = o.daily

	if o.stoppedTime {
		h.handler.now = func() time.Time { return stoppedAt }
	}

	if len(o.triageTurns) > 0 {
		h.triage = modeltest.New(t, model.Capabilities{ToolCalling: true}, o.triageTurns...)
		h.triage.ExpectAllUsed()
		h.handler.opts.Connectors[testTriageConnector] = Connector{Connector: h.triage}

		_, err = pool.Exec(t.Context(),
			`UPDATE investigations SET connectors = connectors || jsonb_build_object('triage', jsonb_build_object('connector', $1::text)) WHERE id = $2`,
			testTriageConnector, testInvestigation)
		require.NoError(t, err)
	}

	return h
}

// newHandler returns a Handler with conn as the claude connector and src
// as the signoz source.
func newHandler(t *testing.T, pool *pgxpool.Pool, conn model.Connector, src source.Source, spans *tracetest.SpanRecorder, reg *prometheus.Registry) *Handler {
	t.Helper()

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key")})
	require.NoError(t, err)

	guard, err := tools.New([]tools.Source{{Name: "signoz", Source: src}}, tools.Options{Redactor: redactor, Logger: slog.New(slog.DiscardHandler)})
	require.NoError(t, err)

	set, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	metrics, err := NewMetrics(reg)
	require.NoError(t, err)

	h, err := New(Options{
		Pool:           pool,
		Guard:          guard,
		Connectors:     map[string]Connector{"claude": {Connector: conn}},
		Prices:         pricing.Shipped(),
		Strategies:     set,
		Routes:         map[string]Route{"payments": {}},
		Rules:          report.Rules{},
		Metrics:        metrics,
		TracerProvider: sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)),
	})
	require.NoError(t, err)

	return h
}

// fakeSignoz returns a source with one tool, search_logs, whose calls all
// return the same text and a dashboard link.
func fakeSignoz(t *testing.T) source.Source {
	t.Helper()

	src := source.NewMockSource(gomock.NewController(t))
	src.EXPECT().Tools(gomock.Any()).Return([]source.Tool{{
		Name:        "search_logs",
		Description: "Search the logs.",
		InputSchema: json.RawMessage(`{"type": "object", "properties": {"query": {"type": "string"}}}`),
		ReadOnly:    true,
	}}, nil).AnyTimes()
	src.EXPECT().Call(gomock.Any(), gomock.Any()).DoAndReturn(func(context.Context, source.Call) (source.Result, error) {
		return source.Result{Text: "error rate 5% at 14:02", Evidence: []source.Evidence{signozDashboard}}, nil
	}).AnyTimes()

	return src
}

// claim claims the investigate job, as a worker does.
func (h *harness) claim(t *testing.T) db.Job {
	t.Helper()

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: testWorker, LeaseSeconds: testLease.Seconds(), Kinds: []string{queue.KindInvestigate}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1, "the investigate job is ready")

	return jobs[0]
}

// run claims the investigate job and runs it, and returns the handler's
// error.
func (h *harness) run(t *testing.T) error {
	t.Helper()

	return h.handler.Handle(t.Context(), h.claim(t))
}

// investigation returns the test investigation.
func (h *harness) investigation(t *testing.T) db.Investigation {
	t.Helper()

	inv, err := db.New(h.pool).GetInvestigation(t.Context(), testInvestigation)
	require.NoError(t, err)

	return inv
}

// report returns the stored report of the test investigation.
func (h *harness) report(t *testing.T) report.Report {
	t.Helper()

	var r report.Report
	require.NoError(t, json.Unmarshal(h.investigation(t).Report, &r))

	return r
}

// steps returns the steps of the test investigation.
func (h *harness) steps(t *testing.T) []db.Step {
	t.Helper()

	steps, err := db.New(h.pool).ListSteps(t.Context(), testInvestigation)
	require.NoError(t, err)

	return steps
}

// publishState returns the stored publish state of the test investigation.
func (h *harness) publishState(t *testing.T) (seq int64, phase string, body publishedBody) {
	t.Helper()

	var raw json.RawMessage
	require.NoError(t, h.pool.QueryRow(t.Context(),
		"SELECT seq, phase, body FROM publish_states WHERE investigation_id = $1", testInvestigation).Scan(&seq, &phase, &raw))
	require.NoError(t, json.Unmarshal(raw, &body))

	return seq, phase, body
}

// expireLease makes the lease of the running investigate job run out and
// reaps it, as the reaper does after a crash.
func (h *harness) expireLease(t *testing.T) {
	t.Helper()

	_, err := h.pool.Exec(t.Context(), "UPDATE jobs SET locked_until = now() - interval '1 second' WHERE kind = 'investigate'")
	require.NoError(t, err)

	reaped, err := db.New(h.pool).ReapExpiredLeases(t.Context())
	require.NoError(t, err)
	require.Len(t, reaped, 1)
}

// publishedBody is publish_states.body as the tests read it.
type publishedBody struct {
	Progress []struct {
		Text string `json:"text"`
	} `json:"progress"`
	Report *report.Report `json:"report"`
}

// summary is each step as "kind status tool", for comparing runs.
func summary(steps []db.Step) []string {
	out := make([]string, 0, len(steps))
	for _, s := range steps {
		out = append(out, s.Kind+" "+s.Status+" "+deref(s.Tool))
	}

	return out
}

// blockingSignoz returns a source like fakeSignoz whose second call does
// not answer until its context ends. blocked is closed when that call
// starts.
func blockingSignoz(t *testing.T, blocked chan<- struct{}) source.Source {
	t.Helper()

	src := source.NewMockSource(gomock.NewController(t))
	src.EXPECT().Tools(gomock.Any()).Return([]source.Tool{{
		Name:        "search_logs",
		InputSchema: json.RawMessage(`{"type": "object"}`),
		ReadOnly:    true,
	}}, nil).AnyTimes()

	first := src.EXPECT().Call(gomock.Any(), gomock.Any()).Return(source.Result{Text: "error rate 5% at 14:02"}, nil)
	src.EXPECT().Call(gomock.Any(), gomock.Any()).After(first).DoAndReturn(func(ctx context.Context, _ source.Call) (source.Result, error) {
		close(blocked)
		<-ctx.Done()

		return source.Result{}, ctx.Err()
	})

	return src
}

// staleRun returns a run of the test investigation as the worker that
// started it with stale.
func staleRun(t *testing.T, h *harness, stale db.Investigation) *run {
	t.Helper()

	hist, err := newHistory(nil)
	require.NoError(t, err)

	return &run{
		h:     h.handler,
		inv:   stale,
		log:   slog.New(slog.DiscardHandler),
		hist:  hist,
		tools: h.handler.opts.Guard.Start(t.Context(), stale.ID.String(), nil),
	}
}

// exhausted returns sidekick_budget_exhausted_total of the payments route
// and budget.
func exhausted(h *harness, budget string) float64 {
	return testutil.ToFloat64(h.handler.opts.Metrics.budgetExhausted.WithLabelValues("payments", budget))
}

// progressTexts returns the text of each published progress line.
func progressTexts(body publishedBody) []string {
	out := make([]string, 0, len(body.Progress))
	for _, p := range body.Progress {
		out = append(out, p.Text)
	}

	return out
}

// lastToolResult returns the last tool result in the last message of req.
func lastToolResult(req model.Request) model.ToolResult {
	var out model.ToolResult

	for _, p := range req.Messages[len(req.Messages)-1].Parts {
		if r, ok := p.(model.ToolResult); ok {
			out = r
		}
	}

	return out
}

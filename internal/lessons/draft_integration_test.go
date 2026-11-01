//go:build integration

package lessons

import (
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/redact"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/internal/summarize"
	"github.com/nourtalaat/sidekick/strategies"
)

// The feedback of the draft tests, and the model they draft with.
var (
	testFeedback = uuid.MustParse("0192d3a4-0000-7000-8000-0000000000f1")
	draftUsage   = model.Usage{InputTokens: 1000, OutputTokens: 200}
)

const (
	draftConnector = "claude-small"
	draftModel     = "claude-small-1"
	// draftCost is what one call costs at the prices of newDraftHarness.
	draftCost = 6000
	// defaultReason is the reason of the feedback of most tests.
	defaultReason = "The pool was fine; the deploy was the cause."
	// goodAnswer is an answer of the model that Sidekick accepts.
	goodAnswer = `{"text": "Check the deploys of the last two hours before the connection pool."}`
)

// draftOptions change what newDraftHarness builds.
type draftOptions struct {
	turns    []modeltest.Turn
	approval bool
	daily    summarize.Daily
	// off builds the Caller without a connector.
	off bool
}

// draftHarness is a DraftJob with a scripted model, on a database seeded
// with fixture/investigations.sql.
type draftHarness struct {
	pool    *pgxpool.Pool
	job     *DraftJob
	conn    *modeltest.Connector
	metrics *Metrics
}

func newDraftHarness(t *testing.T, o draftOptions) *draftHarness {
	t.Helper()

	pool := setup(t)

	conn := modeltest.New(t, model.Capabilities{ToolCalling: true, ToolChoice: true}, o.turns...)
	conn.ExpectAllUsed()

	opts := summarize.Options{
		Name: draftConnector, Model: draftModel, Daily: o.daily,
		Prices: pricing.New(map[string]model.Price{draftModel: {InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000}}),
	}
	if !o.off {
		opts.Connector = conn
	}

	redactor, err := redact.New(redact.Options{HashKey: []byte("test key"), Custom: []redact.Rule{{Name: "ticket", Pattern: `TICKET-[0-9]+`}}})
	require.NoError(t, err)

	set, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	metrics, err := NewMetrics(prometheus.NewRegistry())
	require.NoError(t, err)

	return &draftHarness{
		pool: pool,
		conn: conn,
		job: NewDraftJob(DraftOptions{
			Pool: pool, Caller: summarize.New(opts), Strategies: set, Redactor: redactor,
			RequireApproval: o.approval, Logger: slog.New(slog.DiscardHandler), Metrics: metrics,
		}),
		metrics: metrics,
	}
}

// feedback stores a rating with a reason on the final investigation.
func (h *draftHarness) feedback(t *testing.T, kind, ref, rating, reason string) {
	t.Helper()

	var text *string
	if reason != "" {
		text = &reason
	}

	_, err := h.pool.Exec(t.Context(), `
		INSERT INTO feedback (id, investigation_id, target_kind, target_ref, rating, reason, user_id, channel, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, 'ui', now(), now())`,
		testFeedback, finalInvestigation, kind, ref, rating, text, uuid.New())
	require.NoError(t, err)
}

// run enqueues the draft of the feedback, claims the job and runs it.
func (h *draftHarness) run(t *testing.T, feedbackID uuid.UUID) error {
	t.Helper()

	require.NoError(t, EnqueueDraft(t.Context(), h.pool, feedbackID))

	jobs, err := db.New(h.pool).ClaimJobs(t.Context(), db.ClaimJobsParams{
		Worker: "test-worker", LeaseSeconds: 60, Kinds: []string{queue.KindDraftLesson}, Batch: 1,
	})
	require.NoError(t, err)
	require.Len(t, jobs, 1)

	return h.job.Handle(t.Context(), jobs[0])
}

// storedLesson is a row of lessons as the tests check it.
type storedLesson struct {
	text, status, origin string
	scope                Scope
	feedbackIDs          []uuid.UUID
}

// lessons returns the stored lessons, oldest first.
func (h *draftHarness) lessons(t *testing.T) []storedLesson {
	t.Helper()

	rows, err := h.pool.Query(t.Context(), `
		SELECT text, status, origin, scope_service, scope_alert, scope_route, scope_strategy, feedback_ids
		FROM lessons ORDER BY created_at, id`)
	require.NoError(t, err)

	defer rows.Close()

	var out []storedLesson

	for rows.Next() {
		var (
			l                                  storedLesson
			service, alertName, route, strateg *string
		)

		require.NoError(t, rows.Scan(&l.text, &l.status, &l.origin, &service, &alertName, &route, &strateg, &l.feedbackIDs))

		l.scope = Scope{Service: deref(service), Alert: deref(alertName), Route: deref(route), Strategy: deref(strateg)}
		out = append(out, l)
	}

	require.NoError(t, rows.Err())

	return out
}

// count returns the number of rows the query counts.
func (h *draftHarness) count(t *testing.T, query string, args ...any) int {
	t.Helper()

	var n int

	require.NoError(t, h.pool.QueryRow(t.Context(), query, args...).Scan(&n))

	return n
}

// answerTurn is a model turn that calls submit_lesson with input.
func answerTurn(input string) modeltest.Turn {
	return modeltest.ToolUse(draftUsage, modeltest.Call("s1", SubmitLessonTool, input))
}

// userMessageText returns the text of the user message of a request.
func userMessageText(req model.Request) string {
	var b strings.Builder

	for _, p := range req.Messages[0].Parts {
		if text, ok := p.(model.Text); ok {
			b.WriteString(text.Text)
		}
	}

	return b.String()
}

func TestDraftJob(t *testing.T) {
	t.Parallel()

	both := Scope{Service: "checkout-api", Alert: "CheckoutLatencyP99"}
	wantText := "Check the deploys of the last two hours before the connection pool."

	tests := []struct {
		name string
		opts draftOptions
		// target is the part the feedback rates; the default is the verdict.
		kind, ref string
		turns     []modeltest.Turn
		// wantLessons are the stored lessons in the end.
		wantLessons []storedLesson
		wantCalls   int
		// wantMetric is the result the draft is counted under.
		wantMetric string
		// check looks at the requests the model got.
		check func(t *testing.T, requests []model.Request)
	}{
		{
			name:        "a reason becomes a proposed lesson scoped to the service and alert",
			opts:        draftOptions{approval: true},
			turns:       []modeltest.Turn{answerTurn(goodAnswer)},
			wantLessons: []storedLesson{{text: wantText, status: StatusProposed, origin: "feedback", scope: both, feedbackIDs: []uuid.UUID{testFeedback}}},
			wantCalls:   1,
			wantMetric:  resultProposed,
			check: func(t *testing.T, requests []model.Request) {
				t.Helper()

				req := requests[0]
				require.Equal(t, draftSystem, req.System[0].Text)
				require.Equal(t, SubmitLessonTool, req.Tools[0].Name)
				require.Equal(t, model.ToolChoice{Mode: model.ToolChoiceRequired, Name: SubmitLessonTool}, req.ToolChoice)

				text := userMessageText(req)
				require.Contains(t, text, "Route: payments")
				require.Contains(t, text, "Strategy: generic-alert")
				require.Contains(t, text, "Reason: "+defaultReason)
				require.Contains(t, text, "Verdict: true_positive")
				require.Contains(t, text, "Alert name: CheckoutLatencyP99")
				require.Equal(t, 3, strings.Count(text, "</untrusted_data>"), "feedback, part and event are each wrapped once")
			},
		},
		{
			name:        "without the need for approval the lesson is active",
			opts:        draftOptions{approval: false},
			turns:       []modeltest.Turn{answerTurn(goodAnswer)},
			wantLessons: []storedLesson{{text: wantText, status: StatusActive, origin: "feedback", scope: both, feedbackIDs: []uuid.UUID{testFeedback}}},
			wantCalls:   1,
			wantMetric:  resultActive,
		},
		{
			name:  "the model may widen the lesson to the route",
			opts:  draftOptions{approval: true},
			turns: []modeltest.Turn{answerTurn(`{"text": "Check the deploys.", "scope": {"route": "payments"}}`)},
			wantLessons: []storedLesson{
				{text: "Check the deploys.", status: StatusProposed, origin: "feedback", scope: Scope{Route: "payments"}, feedbackIDs: []uuid.UUID{testFeedback}},
			},
			wantCalls:  1,
			wantMetric: resultProposed,
		},
		{
			name:  "a secret in the text is redacted before it is stored",
			opts:  draftOptions{approval: true},
			turns: []modeltest.Turn{answerTurn(`{"text": "See TICKET-4711."}`)},
			wantLessons: []storedLesson{
				{text: "See [REDACTED:ticket:", status: StatusProposed, origin: "feedback", scope: both, feedbackIDs: []uuid.UUID{testFeedback}},
			},
			wantCalls:  1,
			wantMetric: resultProposed,
		},
		{
			name: "a wrong scope is sent back and the corrected answer is used",
			opts: draftOptions{approval: true},
			turns: []modeltest.Turn{
				answerTurn(`{"text": "Check the deploys.", "scope": {"service": "search-api"}}`),
				answerTurn(`{"text": "Check the deploys.", "scope": {"service": "checkout-api"}}`),
			},
			wantLessons: []storedLesson{
				{text: "Check the deploys.", status: StatusProposed, origin: "feedback", scope: Scope{Service: "checkout-api"}, feedbackIDs: []uuid.UUID{testFeedback}},
			},
			wantCalls:  2,
			wantMetric: resultProposed,
			check: func(t *testing.T, requests []model.Request) {
				t.Helper()

				first, second := userMessageText(requests[0]), userMessageText(requests[1])
				require.NotContains(t, first, "was rejected")
				require.Contains(t, second, "Your last answer was rejected")
				require.Contains(t, second, `scope.service must be "checkout-api"`)
				require.Contains(t, second, "Reason: "+defaultReason, "the correction comes with the original message")
			},
		},
		{
			name: "an answer without the tool call is asked for again",
			opts: draftOptions{approval: true},
			turns: []modeltest.Turn{
				modeltest.Text(draftUsage, "Here is a lesson."),
				answerTurn(goodAnswer),
			},
			wantLessons: []storedLesson{{text: wantText, status: StatusProposed, origin: "feedback", scope: both, feedbackIDs: []uuid.UUID{testFeedback}}},
			wantCalls:   2,
			wantMetric:  resultProposed,
			check: func(t *testing.T, requests []model.Request) {
				t.Helper()

				require.Contains(t, userMessageText(requests[1]), "you did not call submit_lesson")
			},
		},
		{
			name: "three answers that cannot be used store nothing and end the job",
			opts: draftOptions{approval: true},
			turns: []modeltest.Turn{
				answerTurn(`{"text": ""}`),
				answerTurn(`{"text": "` + strings.Repeat("a", 401) + `"}`),
				answerTurn(`{"text": "Fine.", "scope": {"route": "billing"}}`),
			},
			wantCalls:  3,
			wantMetric: resultInvalid,
		},
		{
			name:        "feedback on a finding gives the model the claim",
			opts:        draftOptions{approval: true},
			kind:        "finding",
			ref:         "run:1/finding:f1",
			turns:       []modeltest.Turn{answerTurn(goodAnswer)},
			wantLessons: []storedLesson{{text: wantText, status: StatusProposed, origin: "feedback", scope: both, feedbackIDs: []uuid.UUID{testFeedback}}},
			wantCalls:   1,
			wantMetric:  resultProposed,
			check: func(t *testing.T, requests []model.Request) {
				t.Helper()

				require.Contains(t, userMessageText(requests[0]), "Finding: Errors rose at 14:02.")
			},
		},
		{
			name:        "feedback on a fix gives the model the fix",
			opts:        draftOptions{approval: true},
			kind:        "fix",
			ref:         "run:1/fix:x1",
			turns:       []modeltest.Turn{answerTurn(goodAnswer)},
			wantLessons: []storedLesson{{text: wantText, status: StatusProposed, origin: "feedback", scope: both, feedbackIDs: []uuid.UUID{testFeedback}}},
			wantCalls:   1,
			wantMetric:  resultProposed,
			check: func(t *testing.T, requests []model.Request) {
				t.Helper()

				text := userMessageText(requests[0])
				require.Contains(t, text, "Fix: Roll back 4f2c1a")
				require.Contains(t, text, "Detail: Revert the deploy.")
			},
		},
		{
			name:        "a rated part that is not in the report any more is said so",
			opts:        draftOptions{approval: true},
			kind:        "finding",
			ref:         "run:1/finding:gone",
			turns:       []modeltest.Turn{answerTurn(goodAnswer)},
			wantLessons: []storedLesson{{text: wantText, status: StatusProposed, origin: "feedback", scope: both, feedbackIDs: []uuid.UUID{testFeedback}}},
			wantCalls:   1,
			wantMetric:  resultProposed,
			check: func(t *testing.T, requests []model.Request) {
				t.Helper()

				require.Contains(t, userMessageText(requests[0]), "The rated part is not in the report any more.")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.opts.turns = tt.turns
			h := newDraftHarness(t, tt.opts)

			kind, ref := tt.kind, tt.ref
			if kind == "" {
				kind, ref = "verdict", "run:1/verdict"
			}

			h.feedback(t, kind, ref, "wrong", defaultReason)

			require.NoError(t, h.run(t, testFeedback))
			require.Len(t, h.conn.Requests(), tt.wantCalls)

			got := h.lessons(t)
			require.Len(t, got, len(tt.wantLessons))

			for i, want := range tt.wantLessons {
				require.Equal(t, want.status, got[i].status)
				require.Equal(t, want.origin, got[i].origin)
				require.Equal(t, want.scope, got[i].scope)
				require.Equal(t, want.feedbackIDs, got[i].feedbackIDs)
				require.True(t, strings.HasPrefix(got[i].text, want.text), "text %q starts with %q", got[i].text, want.text)
			}

			require.InDelta(t, 1.0, testutil.ToFloat64(h.metrics.drafts.WithLabelValues(tt.wantMetric)), 0.001)
			require.Equal(t, len(tt.wantLessons), h.count(t, "SELECT count(*) FROM audit_log WHERE action = 'lesson.drafted' AND details->'new'->>'feedbackId' = $1", testFeedback.String()))
			require.Equal(t, int64(tt.wantCalls*draftCost), h.spend(t), "every call is added to the spend of the route")

			if tt.check != nil {
				tt.check(t, h.conn.Requests())
			}
		})
	}
}

func TestDraftJobToolCallFeedback(t *testing.T) {
	t.Parallel()

	h := newDraftHarness(t, draftOptions{approval: true, turns: []modeltest.Turn{answerTurn(goodAnswer)}})

	_, err := h.pool.Exec(t.Context(), `
		INSERT INTO steps (investigation_id, seq, run, kind, parts, source, tool, tool_call_id, status, started_at, duration_ms)
		VALUES ($1, 0, 1, 'tool_result', '[]', 'signoz', 'search_logs', 'call-1', 'no_data', now(), 100)`, finalInvestigation)
	require.NoError(t, err)

	h.feedback(t, "tool_call", "call-1", "wrong", "It searched the wrong service.")

	require.NoError(t, h.run(t, testFeedback))

	text := userMessageText(h.conn.Requests()[0])
	require.Contains(t, text, "Tool: signoz__search_logs")
	require.Contains(t, text, "Result: no_data")
	require.Len(t, h.lessons(t), 1)
}

func TestDraftJobDoesNothing(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts draftOptions
		// setup prepares the database, and returns the feedback to draft.
		setup func(t *testing.T, h *draftHarness) uuid.UUID
		// wantLessons is how many lessons there are in the end.
		wantLessons int
	}{
		{
			name: "feedback that is gone",
			setup: func(*testing.T, *draftHarness) uuid.UUID {
				return uuid.New()
			},
		},
		{
			name: "feedback with no reason",
			setup: func(t *testing.T, h *draftHarness) uuid.UUID {
				t.Helper()
				h.feedback(t, "verdict", "run:1/verdict", "wrong", "")

				return testFeedback
			},
		},
		{
			name: "feedback that a lesson was drafted from already",
			setup: func(t *testing.T, h *draftHarness) uuid.UUID {
				t.Helper()
				h.feedback(t, "verdict", "run:1/verdict", "wrong", defaultReason)

				_, err := h.pool.Exec(t.Context(), `
					INSERT INTO lessons (id, text, scope_service, status, origin, feedback_ids)
					VALUES ($1, 'Drafted before, and rejected since.', 'checkout-api', 'rejected', 'feedback', ARRAY[$2]::uuid[])`,
					uuid.New(), testFeedback)
				require.NoError(t, err)

				return testFeedback
			},
			wantLessons: 1,
		},
		{
			name: "a job that was queued before the summarize role was removed",
			opts: draftOptions{off: true},
			setup: func(t *testing.T, h *draftHarness) uuid.UUID {
				t.Helper()
				h.feedback(t, "verdict", "run:1/verdict", "wrong", defaultReason)

				return testFeedback
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newDraftHarness(t, tt.opts)
			id := tt.setup(t, h)

			require.NoError(t, h.run(t, id))
			require.Empty(t, h.conn.Requests(), "the model is not called")
			require.Len(t, h.lessons(t), tt.wantLessons)
			require.InDelta(t, 1.0, testutil.ToFloat64(h.metrics.drafts.WithLabelValues(resultSkipped)), 0.001)
		})
	}
}

func TestDraftJobModelFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *model.Error
		// wantGiveUp says whether the job ends at once; otherwise the queue
		// retries it.
		wantGiveUp bool
	}{
		{name: "a rate limit is retried", err: &model.Error{Kind: model.ErrRateLimited, Err: errors.New("slow down")}},
		{name: "a timeout is retried", err: &model.Error{Kind: model.ErrTimeout, Err: errors.New("too slow")}},
		{name: "an invalid request ends the job", err: &model.Error{Kind: model.ErrInvalid, Err: errors.New("bad request")}, wantGiveUp: true},
		{name: "a prompt that is too long ends the job", err: &model.Error{Kind: model.ErrContextLength, Err: errors.New("too long")}, wantGiveUp: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := newDraftHarness(t, draftOptions{approval: true, turns: []modeltest.Turn{modeltest.Fail(tt.err)}})
			h.feedback(t, "verdict", "run:1/verdict", "wrong", defaultReason)

			err := h.run(t, testFeedback)
			require.ErrorAs(t, err, new(*model.Error))
			require.Equal(t, tt.wantGiveUp, errors.Is(err, queue.ErrGiveUp))
			require.Empty(t, h.lessons(t))
			require.Zero(t, h.spend(t), "a call that failed costs nothing")
		})
	}
}

func TestDraftJobAtTheDailyCap(t *testing.T) {
	t.Parallel()

	h := newDraftHarness(t, draftOptions{approval: true, daily: summarize.Daily{MaxCostMicros: 1000}})
	h.feedback(t, "verdict", "run:1/verdict", "wrong", defaultReason)

	// Calls today have cost more than the cap.
	_, err := h.pool.Exec(t.Context(), `
		INSERT INTO spend_daily (day, route, connector, model, cost_micros, input_tokens, output_tokens)
		VALUES ((now() AT TIME ZONE 'UTC')::date, 'payments', 'claude', 'claude-opus-5-5', 5000, 1, 1)`)
	require.NoError(t, err)

	require.NoError(t, h.run(t, testFeedback))
	require.Empty(t, h.conn.Requests(), "the model is not called")
	require.Empty(t, h.lessons(t))
	require.InDelta(t, 1.0, testutil.ToFloat64(h.metrics.drafts.WithLabelValues(resultOverCap)), 0.001)

	// The job waits for tomorrow, with the unique key of the feedback.
	require.Equal(t, 1, h.count(t, `
		SELECT count(*) FROM jobs
		WHERE kind = 'draft_lesson' AND status = 'pending' AND run_at > now() AND unique_key = $1`, draftKeyPrefix+testFeedback.String()))
}

func TestEnqueueDraft(t *testing.T) {
	t.Parallel()

	pool := setup(t)

	// A second job for the same feedback is not added while one waits.
	for range 2 {
		require.NoError(t, EnqueueDraft(t.Context(), pool, testFeedback))
	}

	var (
		n       int
		payload string
	)

	require.NoError(t, pool.QueryRow(t.Context(), "SELECT count(*), min(payload::text) FROM jobs WHERE kind = 'draft_lesson'").Scan(&n, &payload))
	require.Equal(t, 1, n)
	require.JSONEq(t, `{"feedbackId": "0192d3a4-0000-7000-8000-0000000000f1"}`, payload)
}

// spend returns what the payments route spent in all, in micro-dollars.
func (h *draftHarness) spend(t *testing.T) int64 {
	t.Helper()

	var micros int64

	require.NoError(t, h.pool.QueryRow(t.Context(),
		"SELECT coalesce(sum(cost_micros), 0)::bigint FROM spend_daily WHERE route = 'payments' AND connector = $1", draftConnector).Scan(&micros))

	return micros
}

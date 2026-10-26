//go:build integration

package investigator

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/model/pricing"
	"github.com/nourtalaat/sidekick/internal/queue"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// Inputs of the built-in tools.
const (
	// goodReport is a true positive that cites call c1 and has one finding
	// without citations.
	goodReport = `{"verdict": "true_positive", "confidence": "high", "reason": "Errors rose after the deploy.",
		"summary": "Errors after a deploy.", "cause": "Deploy 4f2c1a", "impact": "4% of checkouts fail",
		"findings": [
			{"claim": "Errors rose at 14:02.", "evidenceToolCallIds": ["c1"]},
			{"claim": "The deploy caused it.", "evidenceToolCallIds": []}
		],
		"fixes": [{"title": "Roll back 4f2c1a", "detail": "Revert the deploy."}]}`
	// noCauseReport is a true positive without a cause, impact or fix.
	noCauseReport = `{"verdict": "true_positive", "confidence": "high", "reason": "Errors rose.", "summary": "Errors.",
		"findings": [{"claim": "Errors rose at 14:02.", "evidenceToolCallIds": ["c1"]}]}`
	// finding records a finding that cites c1.
	finding = `{"claim": "Errors rose at 14:02.", "evidenceToolCallIds": ["c1"]}`
)

// errBadRequest is a model error that a retry cannot fix.
var errBadRequest = &model.Error{Kind: model.ErrInvalid, Err: errors.New("bad request")}

// search is a source call with the ID id.
func search(id string) model.ToolCall {
	return modeltest.Call(id, searchTool, `{"query": "level:error"}`)
}

// turn is a model turn that makes calls.
func turn(calls ...model.ToolCall) modeltest.Turn {
	return modeltest.ToolUse(testUsage, calls...)
}

// waitForEnd is a model turn that answers only when the context ends.
func waitForEnd() modeltest.Turn {
	return modeltest.Turn{Func: func(ctx context.Context, _ model.Request) (model.Response, error) {
		<-ctx.Done()

		return model.Response{}, &model.Error{Kind: model.ErrTimeout, Err: ctx.Err()}
	}}
}

func TestRun(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		opts   options
		status string
		// steps is summary() of the saved steps.
		steps []string
		check func(t *testing.T, h *harness, r report.Report)
	}{
		{
			name: "a verdict on the first try",
			opts: options{turns: []modeltest.Turn{
				turn(search("c1")),
				turn(modeltest.Call("f1", RecordFindingTool, finding), modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
			}},
			status: StatusFinal,
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				"model_turn ok ", "tool_result ok record_finding", "tool_result ok submit_verdict",
			},
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, report.TruePositive, r.Verdict)
				require.Equal(t, report.StopSubmitted, r.StopReason)
				require.Equal(t, "Deploy 4f2c1a", r.Cause)
				require.Equal(t, []report.Fix{{ID: "x1", Title: "Roll back 4f2c1a", Detail: "Revert the deploy."}}, r.Fixes)
				require.Equal(t, []report.Finding{
					{ID: "f1", Claim: "Errors rose at 14:02.", Evidence: []source.Evidence{{Kind: source.EvidenceMetric, Title: "SigNoz", URL: signozDashboard.URL, ToolCallID: "c1"}}},
					{ID: "f2", Claim: "The deploy caused it."},
				}, r.Findings)
				require.True(t, r.Findings[1].Hypothesis(), "a finding without citations is a hypothesis")
				require.Equal(t, []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 1}}, r.Checked)

				inv := h.investigation(t)
				require.Equal(t, "true_positive", deref(inv.Verdict))
				require.Equal(t, "high", deref(inv.Confidence))
				require.NotNil(t, inv.FirstFindingAt)
				require.NotNil(t, inv.FinishedAt)
				require.EqualValues(t, 1, inv.ToolCalls)

				_, phase, body := h.publishState(t)
				require.Equal(t, "final", phase)
				require.Equal(t, report.TruePositive, body.Report.Verdict)
				require.Equal(t, []string{"Checked signoz (search_logs)", "Finding: Errors rose at 14:02."}, progressTexts(body))
			},
		},
		{
			name: "a bad report fixed on the second try",
			opts: options{turns: []modeltest.Turn{
				turn(search("c1")),
				turn(modeltest.Call("v1", report.SubmitVerdictTool, noCauseReport)),
				turn(modeltest.Call("v2", report.SubmitVerdictTool, goodReport)),
			}},
			status: StatusFinal,
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				"model_turn ok ", "tool_result rejected submit_verdict",
				"model_turn ok ", "tool_result ok submit_verdict",
			},
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, report.TruePositive, r.Verdict)
				require.Equal(t, report.StopSubmitted, r.StopReason)

				rejected := h.steps(t)[4]
				require.Contains(t, deref(rejected.Error), "true_positive needs cause, impact and at least one fix")

				sent := lastToolResult(h.conn.Requests()[2])
				require.True(t, sent.IsError)
				require.Contains(t, sent.Content, "Tries left for submit_verdict: 2.")
			},
		},
		{
			name: "three bad reports end inconclusive",
			opts: options{turns: []modeltest.Turn{
				turn(search("c1"), modeltest.Call("f1", RecordFindingTool, finding)),
				turn(modeltest.Call("v1", report.SubmitVerdictTool, noCauseReport)),
				turn(modeltest.Call("v2", report.SubmitVerdictTool, noCauseReport)),
				turn(modeltest.Call("v3", report.SubmitVerdictTool, `{"verdict": "maybe"}`)),
			}},
			status: StatusFinal,
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs", "tool_result ok record_finding",
				"model_turn ok ", "tool_result rejected submit_verdict",
				"model_turn ok ", "tool_result rejected submit_verdict",
				"model_turn ok ", "tool_result rejected submit_verdict",
			},
			check: func(t *testing.T, _ *harness, r report.Report) {
				t.Helper()

				require.Equal(t, report.Inconclusive, r.Verdict)
				require.Equal(t, report.Low, r.Confidence)
				require.Equal(t, report.StopNoVerdict, r.StopReason)
				require.Equal(t, "The model's report did not pass its checks 3 times.", r.Reason)
				require.Contains(t, r.StopDetail, "the input does not match the tool's schema")
				require.Len(t, r.Findings, 1, "the recorded finding is kept")
				require.False(t, r.Findings[0].Hypothesis())
			},
		},
		{
			name: "no tool call after two reminders ends inconclusive",
			opts: options{turns: []modeltest.Turn{
				modeltest.Text(testUsage, "It looks fine."),
				modeltest.Text(testUsage, "Really, it is fine."),
				modeltest.Text(testUsage, "Fine."),
			}},
			status: StatusFinal,
			steps: []string{
				"prompt ok ", "model_turn ok ", "note ok ", "model_turn ok ", "note ok ", "model_turn ok ",
			},
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, report.Inconclusive, r.Verdict)
				require.Equal(t, report.StopNoVerdict, r.StopReason)
				require.Equal(t, "The model ended its turn without a verdict 3 times.", r.Reason)

				last := h.conn.Requests()[2].Messages
				require.Equal(t, model.Text{Text: reminderText}, last[len(last)-1].Parts[0])
			},
		},
		{
			name: "the tool call limit",
			opts: options{
				budget: Budget{MaxToolCalls: 2, MaxWallTime: time.Minute},
				turns: []modeltest.Turn{
					turn(search("c1")),
					turn(search("c2"), search("c3"), modeltest.Call("f1", RecordFindingTool, finding)),
				},
			},
			status: StatusFinal,
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				"model_turn ok ", "tool_result ok search_logs", "tool_result over_budget signoz__search_logs", "tool_result ok record_finding",
			},
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, report.Inconclusive, r.Verdict)
				require.Equal(t, report.StopBudget, r.StopReason)
				require.Equal(t, "The investigation used all of its 2 tool calls before it reached a verdict.", r.Reason)
				require.Len(t, r.Findings, 1)
				require.Equal(t, []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 2}}, r.Checked)
				require.EqualValues(t, 2, h.investigation(t).ToolCalls)
				require.InDelta(t, 1, exhausted(h, budgetToolCalls), 0)
			},
		},
		{
			name: "the wall time limit",
			opts: options{
				budget: Budget{MaxToolCalls: 40, MaxWallTime: 300 * time.Millisecond},
				turns:  []modeltest.Turn{turn(search("c1")), waitForEnd()},
			},
			status: StatusFinal,
			steps:  []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs"},
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, report.Inconclusive, r.Verdict)
				require.Equal(t, report.StopBudget, r.StopReason)
				require.Equal(t, "The investigation reached its time limit of 300ms before it reached a verdict.", r.Reason)
				require.Equal(t, []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 1}}, r.Checked)
				require.InDelta(t, 1, exhausted(h, budgetWallTime), 0)
			},
		},
		{
			name: "citations that are not earlier source calls are rejected",
			opts: options{turns: []modeltest.Turn{
				turn(search("c1"), modeltest.Call("f1", RecordFindingTool, `{"claim": "Made up.", "evidenceToolCallIds": ["c9", "f0"]}`)),
				turn(modeltest.Call("f2", RecordFindingTool, `{"claim": "From the logs.", "evidenceToolCallIds": ["c1"]}`)),
				turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
			}},
			status: StatusFinal,
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs", "tool_result rejected record_finding",
				"model_turn ok ", "tool_result ok record_finding", "model_turn ok ", "tool_result ok submit_verdict",
			},
			check: func(t *testing.T, h *harness, _ report.Report) {
				t.Helper()

				rejected := h.steps(t)[3]
				require.Contains(t, deref(rejected.Error), `finding 1 cites "c9", which is not the ID of an earlier tool call that returned data`)

				_, _, body := h.publishState(t)
				require.Equal(t, []string{"Checked signoz (search_logs)", "Finding: From the logs."}, progressTexts(body))
			},
		},
		{
			name:   "a request the model refuses fails the investigation",
			opts:   options{turns: []modeltest.Turn{turn(search("c1")), modeltest.Fail(errBadRequest)}},
			status: StatusFailed,
			steps:  []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs"},
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, report.StopError, r.StopReason)
				require.Equal(t, "The model could not take the investigation's request (invalid_request).", r.Reason)

				_, phase, body := h.publishState(t)
				require.Equal(t, "final", phase)
				require.Equal(t, report.StopError, body.Report.StopReason, "the outputs show Could not finish")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.opts)
			require.NoError(t, h.run(t))

			inv := h.investigation(t)
			require.Equal(t, tt.status, inv.Status)

			steps := h.steps(t)
			require.Equal(t, tt.steps, summary(steps))
			require.EqualValues(t, len(steps), inv.NextSeq)

			checkCosts(t, steps, inv)

			seq, _, _ := h.publishState(t)
			require.EqualValues(t, len(steps)+1, seq, "the final update comes after every step's")

			tt.check(t, h, h.report(t))
		})
	}
}

// TestRunResumes stops the worker after step 3, while the source call of
// the second model turn is in flight, and expires its lease; a second
// worker resumes at step 4 by running that call, and the steps have no
// gaps or repeats.
func TestRunResumes(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{turn(search("c1")), turn(search("c2"))}})

	blocked := make(chan struct{})
	h.handler = newHandler(t, h.pool, h.conn, blockingSignoz(t, blocked), h.spans, prometheus.NewRegistry())

	ctx, stop := context.WithCancel(t.Context())

	go func() {
		<-blocked
		stop()
	}()

	require.Error(t, h.handler.Handle(ctx, h.claim(t)), "the stopped worker returns an error")
	require.Equal(t, []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs", "model_turn ok "}, summary(h.steps(t)),
		"steps 0 to 3 are saved")

	h.expireLease(t)

	second := modeltest.New(t, model.Capabilities{ToolCalling: true}, turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)))
	second.ExpectAllUsed()
	h.handler = newHandler(t, h.pool, second, fakeSignoz(t), h.spans, prometheus.NewRegistry())

	require.NoError(t, h.run(t))

	steps := h.steps(t)
	require.Equal(t, []string{
		"prompt ok ", "model_turn ok ", "tool_result ok search_logs", "model_turn ok ",
		"tool_result ok search_logs", "model_turn ok ", "tool_result ok submit_verdict",
	}, summary(steps))

	for i, s := range steps {
		require.EqualValues(t, i, s.Seq, "no gaps or repeats")
	}

	require.Equal(t, "c2", deref(steps[4].ToolCallID), "step 4 answers the call that was in flight")
	require.Len(t, second.Requests()[0].Messages, 5, "the second worker sends the whole saved conversation")

	inv := h.investigation(t)
	require.Equal(t, StatusFinal, inv.Status)
	require.EqualValues(t, len(steps), inv.NextSeq)
	require.EqualValues(t, 2, inv.ToolCalls)
	checkCosts(t, steps, inv)
}

// TestStaleFence checks that a worker whose claim is older than the one
// that owns the run cannot write a step.
func TestStaleFence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// write tries to write as the worker with the stale fence.
		write func(t *testing.T, h *harness, stale db.Investigation) error
	}{
		{
			name: "a step",
			write: func(t *testing.T, h *harness, stale db.Investigation) error {
				t.Helper()

				r := staleRun(t, h, stale)
				_, err := r.save(t.Context(), newStep{kind: kindNote, parts: []model.Part{model.Text{Text: "late"}}, status: statusOK, startedAt: time.Now()})

				return err
			},
		},
		{
			name: "the report",
			write: func(t *testing.T, h *harness, stale db.Investigation) error {
				t.Helper()

				return staleRun(t, h, stale).finish(t.Context(), ending{report: report.Report{Verdict: report.Inconclusive}})
			},
		},
		{
			name: "a new run",
			write: func(t *testing.T, h *harness, stale db.Investigation) error {
				t.Helper()

				_, _, err := h.handler.start(t.Context(), testInvestigation, stale.Fence)

				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{})

			stale, started, err := h.handler.start(t.Context(), testInvestigation, 10)
			require.NoError(t, err)
			require.True(t, started)

			_, started, err = h.handler.start(t.Context(), testInvestigation, 11)
			require.NoError(t, err)
			require.True(t, started, "a newer claim takes over the run")

			require.ErrorIs(t, tt.write(t, h, stale), queue.ErrLeaseLost)

			inv := h.investigation(t)
			require.EqualValues(t, 11, inv.Fence)
			require.Zero(t, inv.NextSeq)
			require.Equal(t, StatusRunning, inv.Status)
			require.Empty(t, h.steps(t))
		})
	}
}

// TestRunEndedInvestigation checks that a job for an investigation that
// has ended completes without running anything.
func TestRunEndedInvestigation(t *testing.T) {
	t.Parallel()

	h := setup(t, options{})

	_, err := h.pool.Exec(t.Context(), "UPDATE investigations SET status = 'final' WHERE id = $1", testInvestigation)
	require.NoError(t, err)

	require.NoError(t, h.run(t))
	require.Empty(t, h.steps(t))
}

// TestRunGivesUp checks that a run whose settings no longer match the
// config gives up its job.
func TestRunGivesUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		update string
	}{
		{name: "the connector is gone", update: `UPDATE investigations SET connectors = '{"investigate": {"connector": "gone"}}'`},
		{name: "the route is gone", update: `UPDATE investigations SET route = 'gone'`},
		{name: "the strategy is gone", update: `UPDATE investigations SET strategy = 'gone'`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, options{})

			_, err := h.pool.Exec(t.Context(), tt.update)
			require.NoError(t, err)

			require.ErrorIs(t, h.run(t), queue.ErrGiveUp)
		})
	}
}

// checkCosts checks that every model step has the cost of its usage at the
// price of the recorded model, and that the investigation's counters are
// the sums of its steps.
func checkCosts(t *testing.T, steps []db.Step, inv db.Investigation) {
	t.Helper()

	price, ok := pricing.Shipped().Lookup(testModel)
	require.True(t, ok)
	require.EqualValues(t, 10_500, price.Cost(testUsage), "4, 20, 0.20 and 5 USD per million tokens")

	var input, output, cost int64

	for _, s := range steps {
		if s.Kind != kindModelTurn {
			require.Nil(t, s.CostMicros, "step %d", s.Seq)

			continue
		}

		usage := model.Usage{
			InputTokens: *s.InputTokens, OutputTokens: *s.OutputTokens,
			CacheReadTokens: *s.CacheReadTokens, CacheWriteTokens: *s.CacheWriteTokens,
		}
		require.Equal(t, testUsage, usage)
		require.Equal(t, price.Cost(usage), *s.CostMicros, "step %d", s.Seq)
		require.Equal(t, "claude", deref(s.Connector))
		require.Equal(t, testModel, deref(s.Model))

		input += *s.InputTokens
		output += *s.OutputTokens
		cost += *s.CostMicros
	}

	require.Equal(t, input, inv.InputTokens)
	require.Equal(t, output, inv.OutputTokens)
	require.Equal(t, cost, inv.CostMicros)
}

// TestRunTelemetry checks the investigation.run span and the investigation
// metrics of a run that ends with a verdict.
func TestRunTelemetry(t *testing.T) {
	t.Parallel()

	h := setup(t, options{turns: []modeltest.Turn{
		turn(search("c1"), modeltest.Call("f1", RecordFindingTool, finding)),
		turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport)),
	}})

	require.NoError(t, h.run(t))

	var runs []map[string]string

	for _, s := range h.spans.Ended() {
		if s.Name() != SpanName {
			continue
		}

		attrs := map[string]string{}
		for _, kv := range s.Attributes() {
			attrs[string(kv.Key)] = kv.Value.String()
		}

		runs = append(runs, attrs)
	}

	require.Equal(t, []map[string]string{{
		attrInvestigationID: testInvestigation.String(),
		attrRoute:           "payments",
		attrStrategy:        "generic-alert",
		attrRun:             "1",
	}}, runs)

	m := h.handler.opts.Metrics
	require.InDelta(t, 1, testutil.ToFloat64(m.investigations.WithLabelValues("payments", "true_positive", "submitted")), 0)
	require.InDelta(t, 0, testutil.ToFloat64(m.runningRuns.WithLabelValues("payments")), 0)
	require.Equal(t, 1, testutil.CollectAndCount(m.duration))
	require.Equal(t, 1, testutil.CollectAndCount(m.firstFindings))
}

//go:build integration

package investigator

import (
	"context"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// Each scripted turn uses testUsage: 6,500 tokens with the cache, and
// 10,500 micro-dollars at the price of testModel.
const (
	turnTokens = 6_500
	turnCost   = 10_500
)

// finalNote is the summary of the note that starts the final turn.
const finalNote = "note ok "

// waitForEnd is a model turn that answers only when the context ends.
func waitForEnd() modeltest.Turn {
	return modeltest.Turn{Func: func(ctx context.Context, _ model.Request) (model.Response, error) {
		<-ctx.Done()

		return model.Response{}, &model.Error{Kind: model.ErrTimeout, Err: ctx.Err()}
	}}
}

// uncitedReport is a true positive whose only finding cites no call.
const uncitedReport = `{"verdict": "true_positive", "confidence": "medium", "reason": "Errors rose after the deploy.",
	"summary": "Errors after a deploy.", "cause": "Deploy 4f2c1a", "impact": "4% of checkouts fail",
	"findings": [{"claim": "The deploy caused it.", "evidenceToolCallIds": []}],
	"fixes": [{"title": "Roll back 4f2c1a", "detail": "Revert the deploy."}]}`

// submit is a final turn that submits goodReport.
func submit() modeltest.Turn {
	return turn(modeltest.Call("v1", report.SubmitVerdictTool, goodReport))
}

// TestBudgets runs each budget to its soft limit, where the model gets a
// final turn with only submit_verdict, and to its hard limit, where the
// report is built from the saved steps.
func TestBudgets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts options
		// steps is summary() of the saved steps.
		steps []string
		// budget is the budget that ran out.
		budget string
		// final means the model submitted its report in the final turn.
		final  bool
		reason string
		detail string
		check  func(t *testing.T, h *harness, r report.Report)
	}{
		{
			name: "tokens: the final turn submits a report",
			opts: options{
				budget:      Budget{MaxTokens: turnTokens + 500, MaxWallTime: time.Minute},
				stoppedTime: true,
				caps:        model.Capabilities{ToolChoice: true},
				turns:       []modeltest.Turn{turn(search("c1")), submit()},
			},
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				finalNote, "model_turn ok ", "tool_result ok submit_verdict",
			},
			budget: budgetTokens,
			final:  true,
			reason: "Errors rose after the deploy.",
			detail: "maxTokens 7000 nearly reached; the verdict submitted in the final turn was true_positive",
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, "Deploy 4f2c1a", r.Cause, "the model's report is kept")
				require.Len(t, r.Findings, 2)

				final := h.conn.Requests()[1]
				require.Equal(t, []string{report.SubmitVerdictTool}, toolNames(final), "only submit_verdict is offered")
				require.Equal(t, model.ToolChoice{Mode: model.ToolChoiceRequired, Name: report.SubmitVerdictTool}, final.ToolChoice)

				last := final.Messages[len(final.Messages)-1].Parts
				require.Equal(t, model.Text{Text: "This investigation has nearly used its token budget. This is your last turn, and only " +
					"submit_verdict is available. Call submit_verdict now with what the evidence so far supports. The verdict " +
					"will be recorded as inconclusive, with your findings and reasoning."}, last[len(last)-1])

				require.Equal(t, budgetTokens, deref(h.steps(t)[3].Budget))
			},
		},
		{
			name: "tokens: used up by one turn, without a final turn",
			opts: options{
				budget:      Budget{MaxTokens: turnTokens, MaxWallTime: time.Minute},
				stoppedTime: true,
				turns:       []modeltest.Turn{turn(search("c1"))},
			},
			steps:  []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs"},
			budget: budgetTokens,
			reason: "The investigation used its budget of 6500 tokens before it reached a verdict.",
			detail: "maxTokens 6500 reached",
		},
		{
			name: "cost: the final turn submits a report without a forced tool choice",
			opts: options{
				budget:      Budget{MaxCostMicros: turnCost + 500, MaxWallTime: time.Minute},
				stoppedTime: true,
				turns:       []modeltest.Turn{turn(search("c1")), submit()},
			},
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				finalNote, "model_turn ok ", "tool_result ok submit_verdict",
			},
			budget: budgetCost,
			final:  true,
			reason: "Errors rose after the deploy.",
			detail: "maxCostUSD 0.01 nearly reached; the verdict submitted in the final turn was true_positive",
			check: func(t *testing.T, h *harness, _ report.Report) {
				t.Helper()

				final := h.conn.Requests()[1]
				require.Equal(t, []string{report.SubmitVerdictTool}, toolNames(final))
				require.Equal(t, model.ToolChoice{}, final.ToolChoice, "the connector cannot force a tool")
			},
		},
		{
			name: "cost: used up by one turn, without a final turn",
			opts: options{
				budget:      Budget{MaxCostMicros: turnCost - 500, MaxWallTime: time.Minute},
				stoppedTime: true,
				turns:       []modeltest.Turn{turn(search("c1"))},
			},
			steps:  []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs"},
			budget: budgetCost,
			reason: "The investigation used its cost budget of $0.01 before it reached a verdict.",
			detail: "maxCostUSD 0.01 reached",
		},
		{
			name: "tool calls: a call past the limit, then the final turn",
			opts: options{
				budget:      Budget{MaxToolCalls: 2, MaxWallTime: time.Minute},
				stoppedTime: true,
				turns: []modeltest.Turn{
					turn(search("c1")),
					turn(search("c2"), search("c3"), modeltest.Call("f1", RecordFindingTool, finding)),
					submit(),
				},
			},
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				"model_turn ok ", "tool_result ok search_logs", "tool_result over_budget signoz__search_logs", "tool_result ok record_finding",
				finalNote, "model_turn ok ", "tool_result ok submit_verdict",
			},
			budget: budgetToolCalls,
			final:  true,
			reason: "Errors rose after the deploy.",
			detail: "maxToolCalls 2 nearly reached; the verdict submitted in the final turn was true_positive",
			check: func(t *testing.T, h *harness, _ report.Report) {
				t.Helper()

				require.EqualValues(t, 2, h.investigation(t).ToolCalls)
			},
		},
		{
			name: "tool calls: the final turn answers with text",
			opts: options{
				budget:      Budget{MaxToolCalls: 2, MaxWallTime: time.Minute},
				stoppedTime: true,
				turns: []modeltest.Turn{
					turn(search("c1"), search("c2"), modeltest.Call("f1", RecordFindingTool, finding)),
					modeltest.Text(testUsage, "It is the deploy."),
				},
			},
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs", "tool_result ok search_logs", "tool_result ok record_finding",
				finalNote, "model_turn ok ",
			},
			budget: budgetToolCalls,
			reason: "The investigation used all of its 2 tool calls before it reached a verdict.",
			detail: "maxToolCalls 2 reached",
			check: func(t *testing.T, _ *harness, r report.Report) {
				t.Helper()

				require.Len(t, r.Findings, 1, "the recorded finding is kept")
				require.Equal(t, []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 2}}, r.Checked)
			},
		},
		{
			name: "tokens: the final turn calls a source and sends a bad report",
			opts: options{
				budget:      Budget{MaxTokens: turnTokens + 500, MaxWallTime: time.Minute},
				stoppedTime: true,
				turns: []modeltest.Turn{
					turn(search("c1")),
					turn(search("c2"), modeltest.Call("v1", report.SubmitVerdictTool, noCauseReport)),
				},
			},
			steps: []string{
				"prompt ok ", "model_turn ok ", "tool_result ok search_logs",
				finalNote, "model_turn ok ", "tool_result rejected signoz__search_logs", "tool_result rejected submit_verdict",
			},
			budget: budgetTokens,
			reason: "The investigation used its budget of 7000 tokens before it reached a verdict.",
			detail: "maxTokens 7000 reached",
			check: func(t *testing.T, h *harness, r report.Report) {
				t.Helper()

				require.Equal(t, []report.Check{{Source: "signoz", Status: report.CheckOK, ToolCalls: 1}}, r.Checked,
					"the source is not called in the final turn")
				require.EqualValues(t, 1, h.investigation(t).ToolCalls)
			},
		},
		{
			name: "wall time: the final turn starts a minute before the end",
			opts: options{
				budget:     Budget{MaxWallTime: 10 * time.Minute},
				startedAgo: 9*time.Minute + 30*time.Second,
				turns:      []modeltest.Turn{turn(modeltest.Call("v1", report.SubmitVerdictTool, uncitedReport))},
			},
			steps:  []string{"prompt ok ", finalNote, "model_turn ok ", "tool_result ok submit_verdict"},
			budget: budgetWallTime,
			final:  true,
			reason: "Errors rose after the deploy.",
			detail: "maxWallTime 10m0s nearly reached; the verdict submitted in the final turn was true_positive",
		},
		{
			// The clock of the soft limits stands still, so the final turn
			// never starts and the model call waits for the deadline, however
			// long the steps before it take.
			name: "wall time: the deadline passes during a model call",
			opts: options{
				budget:      Budget{MaxToolCalls: 40, MaxWallTime: 300 * time.Millisecond},
				turns:       []modeltest.Turn{waitForEnd()},
				stoppedTime: true,
			},
			steps:  []string{"prompt ok "},
			budget: budgetWallTime,
			reason: "The investigation reached its time limit of 300ms before it reached a verdict.",
			detail: "maxWallTime 300ms reached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := setup(t, tt.opts)
			require.NoError(t, h.run(t))

			inv := h.investigation(t)
			require.Equal(t, StatusFinal, inv.Status)

			steps := h.steps(t)
			require.Equal(t, tt.steps, summary(steps))
			checkCosts(t, steps, inv)

			r := h.report(t)
			require.Equal(t, report.Inconclusive, r.Verdict)
			require.Equal(t, report.Low, r.Confidence)
			require.Equal(t, report.StopBudget, r.StopReason)
			require.Equal(t, tt.reason, r.Reason)
			require.Equal(t, tt.detail, r.StopDetail)
			require.InDelta(t, 1, exhausted(h, tt.budget), 0)

			_, phase, body := h.publishState(t)
			require.Equal(t, "final", phase)
			require.Equal(t, report.StopBudget, body.Report.StopReason)

			if tt.check != nil {
				tt.check(t, h, r)
			}
		})
	}
}

// TestFinalTurnResumes stops the worker during the final turn's model call
// and checks that the next worker sends the final turn again, from the
// saved note, instead of starting a new one.
func TestFinalTurnResumes(t *testing.T) {
	t.Parallel()

	called := make(chan struct{})
	h := setup(t, options{
		budget:      Budget{MaxTokens: turnTokens + 500, MaxWallTime: time.Minute},
		stoppedTime: true,
		turns: []modeltest.Turn{turn(search("c1")), {Func: func(ctx context.Context, _ model.Request) (model.Response, error) {
			close(called)
			<-ctx.Done()

			return model.Response{}, &model.Error{Kind: model.ErrTimeout, Err: ctx.Err()}
		}}},
	})

	ctx, stop := context.WithCancel(t.Context())

	go func() {
		<-called
		stop()
	}()

	require.Error(t, h.handler.Handle(ctx, h.claim(t)), "the stopped worker returns an error")
	require.Equal(t, []string{"prompt ok ", "model_turn ok ", "tool_result ok search_logs", finalNote}, summary(h.steps(t)))

	h.expireLease(t)

	second := modeltest.New(t, model.Capabilities{ToolCalling: true}, submit())
	second.ExpectAllUsed()
	h.handler = newHandler(t, h.pool, second, fakeSignoz(t), h.spans, prometheus.NewRegistry())
	h.handler.now = func() time.Time { return stoppedAt }

	require.NoError(t, h.run(t))
	require.Equal(t, []string{
		"prompt ok ", "model_turn ok ", "tool_result ok search_logs", finalNote, "model_turn ok ", "tool_result ok submit_verdict",
	}, summary(h.steps(t)))
	require.Equal(t, []string{report.SubmitVerdictTool}, toolNames(second.Requests()[0]))
	require.Equal(t, report.StopBudget, h.report(t).StopReason)
}

// toolNames returns the names of the tools offered in req.
func toolNames(req model.Request) []string {
	out := make([]string, 0, len(req.Tools))
	for _, tool := range req.Tools {
		out = append(out, tool.Name)
	}

	return out
}

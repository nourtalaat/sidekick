package investigator

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestSubmitted(t *testing.T) {
	t.Parallel()

	logs := source.Evidence{Kind: source.EvidenceLogQuery, Title: "Logs", URL: "https://logs.example.com/q", ToolCallID: "c1"}

	tests := []struct {
		name         string
		input        string
		rules        report.Rules
		want         report.Report
		wantProblems []string
	}{
		{
			name: "a false positive with evidence",
			input: `{"verdict": "false_positive", "confidence": "high", "reason": "A batch job.", "summary": "Batch.",
				"findings": [{"claim": "The job ran at 14:00.", "evidenceToolCallIds": ["c1", "c2"]}],
				"falsePositive": {"whyFired": "Batch load.", "prevention": [{"kind": "duration", "detail": "Wait 5m."}]}}`,
			want: report.Report{
				Verdict: report.FalsePositive, Confidence: report.High, Reason: "A batch job.", Summary: "Batch.",
				Findings: []report.Finding{{ID: "f1", Claim: "The job ran at 14:00.", Evidence: []source.Evidence{
					logs,
					{Kind: source.EvidenceOther, Title: "signoz search_logs", ToolCallID: "c2"},
				}}},
				FalsePositive: &report.FalsePositiveDetail{WhyFired: "Batch load.", Prevention: []report.Prevention{{Kind: report.PreventDuration, Detail: "Wait 5m."}}},
			},
		},
		{
			name: "a false positive below the default confidence",
			input: `{"verdict": "false_positive", "confidence": "medium", "reason": "r", "summary": "s", "findings": [],
				"falsePositive": {"whyFired": "w", "prevention": [{"kind": "other", "detail": "d"}]}}`,
			wantProblems: []string{"invalid report: false_positive needs confidence high or higher; gather more evidence or use inconclusive"},
		},
		{
			name: "a false positive at a lowered confidence",
			input: `{"verdict": "false_positive", "confidence": "medium", "reason": "r", "summary": "s", "findings": [],
				"falsePositive": {"whyFired": "w", "prevention": [{"kind": "other", "detail": "d"}]}}`,
			rules: report.Rules{MinFalsePositiveConfidence: report.Medium},
		},
		{
			name:  "citations of a failed call, a built-in call and an unknown call",
			input: `{"verdict": "inconclusive", "confidence": "low", "reason": "r", "summary": "s", "findings": [{"claim": "x", "evidenceToolCallIds": ["c3", "f1", "nope"]}]}`,
			wantProblems: []string{
				`finding 1 cites "c3", which is not the ID of an earlier tool call that returned data`,
				`finding 1 cites "f1", which is not the ID of an earlier tool call that returned data`,
				`finding 1 cites "nope", which is not the ID of an earlier tool call that returned data`,
			},
		},
		{
			name:         "input that does not match the schema",
			input:        `{"verdict": "maybe"}`,
			wantProblems: []string{"the input does not match the tool's schema"},
		},
	}

	b := testBuiltins(t)
	h := citedHistory(t, logs)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, problems := b.submitted(h, model.ToolCall{ID: "v1", Name: report.SubmitVerdictTool, Input: json.RawMessage(tt.input)}, tt.rules)

			if len(tt.wantProblems) > 0 {
				require.Len(t, problems, len(tt.wantProblems))

				for i, p := range tt.wantProblems {
					require.Contains(t, problems[i], p)
				}

				return
			}

			require.Empty(t, problems)

			if tt.want.Verdict != "" {
				require.Equal(t, tt.want, got)
			}
		})
	}
}

func TestRecorded(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        string
		want         report.Finding
		wantProblems int
	}{
		{name: "a hypothesis", input: `{"claim": "Maybe the deploy.", "evidenceToolCallIds": []}`, want: report.Finding{ID: "f3", Claim: "Maybe the deploy."}},
		{name: "an unknown citation", input: `{"claim": "x", "evidenceToolCallIds": ["c9"]}`, wantProblems: 1},
		{name: "no citations field", input: `{"claim": "x"}`, wantProblems: 1},
	}

	b := testBuiltins(t)
	h := citedHistory(t, source.Evidence{})

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, problems := b.recorded(h, model.ToolCall{ID: "f9", Name: RecordFindingTool, Input: json.RawMessage(tt.input)}, 2)
			require.Len(t, problems, tt.wantProblems)

			if tt.wantProblems == 0 {
				require.Equal(t, tt.want, got)
				require.True(t, got.Hypothesis())
			}
		})
	}
}

func TestRejectedText(t *testing.T) {
	t.Parallel()

	require.Equal(t, "The report did not pass its checks. Fix these and call the tool again:\n- one\n- two",
		rejectedText("report", []string{"one", "two"}))
}

// testBuiltins returns the built-in tools with their schemas.
func testBuiltins(t *testing.T) builtins {
	t.Helper()

	submit, err := tools.ParseInputSchema(report.SubmitVerdictSchema)
	require.NoError(t, err)

	record, err := tools.ParseInputSchema(recordFindingSchema)
	require.NoError(t, err)

	return builtins{submitVerdict: submit, recordFinding: record}
}

// citedHistory returns a history with four results: c1, a source call
// with evidence links (when links has a URL), c2, one without, c3, a
// failed one, and f1, a recorded finding.
func citedHistory(t *testing.T, links source.Evidence) *history {
	t.Helper()

	raw, err := encodePrompt(model.Request{})
	require.NoError(t, err)

	c1 := resultStep(t, model.ToolResult{CallID: "c1"})
	if links.URL != "" {
		c1.Evidence, err = json.Marshal([]source.Evidence{links})
		require.NoError(t, err)
	}

	c3 := resultStep(t, model.ToolResult{CallID: "c3", IsError: true})
	c3.Status = statusTimeout

	f1 := partsStep(t, kindToolResult, model.ToolResult{CallID: "f1"})
	f1.Tool, f1.ToolCallID = new(RecordFindingTool), new("f1")

	steps := []db.Step{
		{Kind: kindPrompt, Parts: raw},
		partsStep(t, kindModelTurn,
			model.ToolCall{ID: "c1", Name: searchTool}, model.ToolCall{ID: "c2", Name: searchTool}, model.ToolCall{ID: "c3", Name: searchTool},
			model.ToolCall{ID: "f1", Name: RecordFindingTool, Input: json.RawMessage(`{"claim": "seen", "evidenceToolCallIds": []}`)}),
		c1, resultStep(t, model.ToolResult{CallID: "c2"}), c3, f1,
	}

	for i := range steps {
		steps[i].Seq = int32(i) //nolint:gosec // A handful of steps.
	}

	h, err := newHistory(steps)
	require.NoError(t, err)

	return h
}

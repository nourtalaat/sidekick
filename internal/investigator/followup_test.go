package investigator

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

func TestFollowUpHistory(t *testing.T) {
	t.Parallel()

	prompt := model.Request{
		System:   []model.Text{{Text: "system"}},
		Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "event"}}}},
	}

	long := func(s string) string { return strings.Repeat(s, minSummaryBytes) }

	c0 := model.ToolCall{ID: "c0", Name: searchTool, Input: json.RawMessage(`{}`)}
	c1 := model.ToolCall{ID: "c1", Name: searchTool, Input: json.RawMessage(`{}`)}
	v1 := model.ToolCall{ID: "v1", Name: report.SubmitVerdictTool, Input: json.RawMessage(`{}`)}
	c2 := model.ToolCall{ID: "c2", Name: searchTool, Input: json.RawMessage(`{}`)}
	r0 := model.ToolResult{CallID: "c0", Content: long("a")}
	r1 := model.ToolResult{CallID: "c1", Content: long("b")}
	rv1 := model.ToolResult{CallID: "v1", Content: "The report was accepted."}
	r2 := model.ToolResult{CallID: "c2", Content: long("c")}
	question := model.Text{Text: "# Follow-up questions"}
	summary := model.ToolResult{CallID: "c0", Content: "short"}

	// The first run: two long results, then a verdict.
	first := []db.Step{
		partsStep(t, kindModelTurn, c0, c1),
		resultStep(t, r0),
		resultStep(t, r1),
		partsStep(t, kindModelTurn, v1),
		builtinStep(t, rv1, report.SubmitVerdictTool),
	}

	tests := []struct {
		name string
		// followUp are the steps of the second run, which has started.
		followUp []db.Step
		// want are the messages after the prompt's.
		want         []model.Message
		wantRunSteps int
		wantOld      []string
		wantReminder bool
	}{
		{
			name: "a follow-up run that has saved nothing yet",
			want: []model.Message{
				{Role: model.Assistant, Parts: []model.Part{c0, c1}},
				{Role: model.User, Parts: []model.Part{r0, r1}},
				{Role: model.Assistant, Parts: []model.Part{v1}},
				{Role: model.User, Parts: []model.Part{rv1}},
			},
			wantOld: []string{"c0", "c1"},
		},
		{
			name:     "the question joins the last user message",
			followUp: []db.Step{partsStep(t, kindInbox, question)},
			want: []model.Message{
				{Role: model.Assistant, Parts: []model.Part{c0, c1}},
				{Role: model.User, Parts: []model.Part{r0, r1}},
				{Role: model.Assistant, Parts: []model.Part{v1}},
				{Role: model.User, Parts: []model.Part{rv1, question}},
			},
			wantRunSteps: 1,
			wantOld:      []string{"c0", "c1"},
		},
		{
			name: "a summary replaces the oldest result in place, and the last turn's result is kept",
			followUp: []db.Step{
				partsStep(t, kindInbox, question),
				partsStep(t, kindModelTurn, c2),
				resultStep(t, r2),
				summaryStep(t, summary),
			},
			want: []model.Message{
				{Role: model.Assistant, Parts: []model.Part{c0, c1}},
				{Role: model.User, Parts: []model.Part{summary, r1}},
				{Role: model.Assistant, Parts: []model.Part{v1}},
				{Role: model.User, Parts: []model.Part{rv1, question}},
				{Role: model.Assistant, Parts: []model.Part{c2}},
				{Role: model.User, Parts: []model.Part{r2}},
			},
			wantRunSteps: 4,
			wantOld:      []string{"c1"},
		},
		{
			name: "a turn without a tool call in the follow-up run needs a reminder",
			followUp: []db.Step{
				partsStep(t, kindInbox, question),
				partsStep(t, kindModelTurn, model.Text{Text: "It was the deploy."}),
			},
			want: []model.Message{
				{Role: model.Assistant, Parts: []model.Part{c0, c1}},
				{Role: model.User, Parts: []model.Part{r0, r1}},
				{Role: model.Assistant, Parts: []model.Part{v1}},
				{Role: model.User, Parts: []model.Part{rv1, question}},
				{Role: model.Assistant, Parts: []model.Part{model.Text{Text: "It was the deploy."}}},
			},
			wantRunSteps: 2,
			wantOld:      []string{"c0", "c1"},
			wantReminder: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := encodePrompt(prompt)
			require.NoError(t, err)

			steps := []db.Step{{Kind: kindPrompt, Parts: raw, Status: statusOK, StartedAt: testStart, Run: 1}}
			steps = append(steps, withRun(first, 1)...)
			steps = append(steps, withRun(tt.followUp, 2)...)

			for i := range steps {
				steps[i].Seq = int32(i) //nolint:gosec // A handful of steps.
			}

			h, err := newHistory(steps)
			require.NoError(t, err)

			h.startRun(2)

			require.Equal(t, append(prompt.Messages, tt.want...), h.request().Messages)
			require.Len(t, h.runSteps(), tt.wantRunSteps)
			require.Equal(t, len(first)+1, h.runFrom)
			require.Equal(t, tt.wantReminder, h.needsReminder())
			require.Zero(t, h.count(func(s db.Step) bool { return isBuiltin(s, report.SubmitVerdictTool) }),
				"the first run's verdict does not count in the follow-up run")

			var old []string

			for _, o := range h.oldResults() {
				old = append(old, o.result.CallID)
			}

			require.Equal(t, tt.wantOld, old)
		})
	}
}

func TestHistoryRejectsBadSummaries(t *testing.T) {
	t.Parallel()

	raw, err := encodePrompt(model.Request{})
	require.NoError(t, err)

	prompt := db.Step{Kind: kindPrompt, Parts: raw}

	tests := []struct {
		name    string
		summary db.Step
	}{
		{name: "a summary of a call with no result", summary: summaryStep(t, model.ToolResult{CallID: "c9", Content: "short"})},
		{name: "a summary that is not a tool result", summary: partsStep(t, kindSummary, model.Text{Text: "short"})},
		{name: "a summary of two results", summary: partsStep(t, kindSummary, model.ToolResult{CallID: "c9"}, model.ToolResult{CallID: "c8"})},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := newHistory([]db.Step{prompt, withSeq(tt.summary, 1)})
			require.Error(t, err)
		})
	}
}

func TestWithoutOtherReasoning(t *testing.T) {
	t.Parallel()

	anthropic := model.Reasoning{Provider: "anthropic", Text: "Think.", Signature: "sig"}
	openai := model.Reasoning{Provider: "openai", ID: "rs_1", Signature: "enc"}
	call := model.ToolCall{ID: "c1", Name: searchTool, Input: json.RawMessage(`{}`)}

	tests := []struct {
		name     string
		provider string
		want     []model.Part
	}{
		{name: "the same provider gets its reasoning back", provider: "anthropic", want: []model.Part{anthropic, call}},
		{name: "a new provider gets none of another's", provider: "bedrock", want: []model.Part{call}},
		{name: "openai keeps only its own", provider: "openai", want: []model.Part{openai, call}},
		{name: "an unknown provider keeps everything", provider: "", want: []model.Part{anthropic, openai, call}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := model.Request{Messages: []model.Message{
				{Role: model.User, Parts: []model.Part{model.Text{Text: "event"}}},
				{Role: model.Assistant, Parts: []model.Part{anthropic, openai, call}},
			}}

			got := withoutOtherReasoning(req, tt.provider)
			require.Equal(t, tt.want, got.Messages[1].Parts)
			require.Equal(t, []model.Part{model.Text{Text: "event"}}, got.Messages[0].Parts)
		})
	}
}

func TestBudgetFollowUp(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		budget Budget
		want   Budget
	}{
		{
			name:   "half of every limit",
			budget: Budget{MaxTokens: 400_000, MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostMicros: 1_000_000},
			want:   Budget{MaxTokens: 200_000, MaxToolCalls: 20, MaxWallTime: 5 * time.Minute, MaxCostMicros: 500_000},
		},
		{name: "no limit stays no limit", budget: Budget{MaxToolCalls: 40}, want: Budget{MaxToolCalls: 20}},
		{name: "a limit of one stays one", budget: Budget{MaxToolCalls: 1}, want: Budget{MaxToolCalls: 1}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.budget.FollowUp())
		})
	}
}

func TestFollowUpRunUse(t *testing.T) {
	t.Parallel()

	now := testStart.Add(time.Hour)
	inv := db.Investigation{CostMicros: 50_000, ToolCalls: 7, Run: 2}

	call := resultStep(t, model.ToolResult{CallID: "c5"})

	overBudget := resultStep(t, model.ToolResult{CallID: "c6"})
	overBudget.Status = statusOverBudget

	finalOnly := resultStep(t, model.ToolResult{CallID: "c7"})
	finalOnly.Status, finalOnly.Error = statusRejected, new(finalTurnOnlyError)

	turn := partsStep(t, kindModelTurn, model.Text{Text: "x"})
	turn.CostMicros = new(int64(10_000))
	turn.StartedAt = testStart.Add(30 * time.Minute)

	tests := []struct {
		name       string
		steps      []db.Step
		wantStart  time.Time
		wantBefore usage
	}{
		{name: "a new run starts now", wantStart: now, wantBefore: usage{costMicros: 50_000, toolCalls: 7}},
		{
			name:       "a resumed run counts only its own steps",
			steps:      []db.Step{turn, call, overBudget, finalOnly, builtinStep(t, model.ToolResult{CallID: "v1"}, report.AnswerFollowUpTool)},
			wantStart:  turn.StartedAt,
			wantBefore: usage{costMicros: 40_000, toolCalls: 6},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw, err := encodePrompt(model.Request{})
			require.NoError(t, err)

			steps := []db.Step{{Kind: kindPrompt, Parts: raw, Status: statusOK, StartedAt: testStart, Run: 1}}
			steps = append(steps, withRun(tt.steps, 2)...)

			for i := range steps {
				steps[i].Seq = int32(i) //nolint:gosec // A handful of steps.
			}

			h, err := newHistory(steps)
			require.NoError(t, err)

			h.startRun(2)

			start, before := h.runUse(now, inv)
			require.Equal(t, tt.wantStart, start)
			require.Equal(t, tt.wantBefore, before)
		})
	}
}

func TestFollowUpBudgetReached(t *testing.T) {
	t.Parallel()

	runStart := testStart.Add(time.Hour)

	tests := []struct {
		name    string
		inv     db.Investigation
		before  usage
		elapsed time.Duration
		want    string
	}{
		{
			name:   "the first run's use does not count",
			inv:    db.Investigation{CostMicros: 900_000, ToolCalls: 36},
			before: usage{costMicros: 900_000, toolCalls: 36},
		},
		{
			name:   "the run's own cost at the soft limit",
			inv:    db.Investigation{CostMicros: 1_350_000},
			before: usage{costMicros: 900_000},
			want:   budgetCost,
		},
		{
			name:   "the run's own tool calls at the soft limit",
			inv:    db.Investigation{ToolCalls: 54},
			before: usage{toolCalls: 36},
			want:   budgetToolCalls,
		},
		{
			name:    "the wall time counts from the run's start: 30 seconds before its 5 minutes",
			elapsed: 4*time.Minute + 31*time.Second,
			want:    budgetWallTime,
		},
		{
			name:    "just before the soft limit of the run's wall time",
			elapsed: 4*time.Minute + 29*time.Second,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			hist, err := newHistory(nil)
			require.NoError(t, err)

			inv := tt.inv
			inv.StartedAt = &testStart

			budget := Budget{MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostMicros: 1_000_000}.FollowUp()
			r := &run{budget: budget, inv: inv, hist: hist, runStart: runStart, before: tt.before}

			got, hard := r.budgetReached(runStart.Add(tt.elapsed), false)
			require.Equal(t, tt.want, got)
			require.False(t, hard)
		})
	}
}

// builtinStep returns the result step of a call to the built-in tool name.
func builtinStep(t *testing.T, res model.ToolResult, name string) db.Step {
	t.Helper()

	s := partsStep(t, kindToolResult, res)
	s.Tool, s.ToolCallID = new(name), new(res.CallID)

	return s
}

// summaryStep returns a summary step that replaces the result of a signoz
// search with res.
func summaryStep(t *testing.T, res model.ToolResult) db.Step {
	t.Helper()

	s := partsStep(t, kindSummary, res)
	s.Source, s.Tool, s.ToolCallID = new("signoz"), new("search_logs"), new(res.CallID)

	return s
}

// withRun returns a copy of steps in run.
func withRun(steps []db.Step, run int32) []db.Step {
	out := make([]db.Step, len(steps))
	for i, s := range steps {
		s.Run = run
		out[i] = s
	}

	return out
}

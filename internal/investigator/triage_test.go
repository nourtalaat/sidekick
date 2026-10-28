package investigator

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/investigator/tools"
	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/prompt"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/store/db"
	"github.com/nourtalaat/sidekick/internal/strategy"
	"github.com/nourtalaat/sidekick/strategies"
)

// triageUsage is the token usage of every scripted triage turn.
var triageUsage = model.Usage{InputTokens: 800, OutputTokens: 60}

func TestTriagerAsk(t *testing.T) {
	t.Parallel()

	tr := newTestTriager(t)

	tests := []struct {
		name string
		turn modeltest.Turn
		caps model.Capabilities
		want triageDecision
		// wantToolChoice is the tool choice the request must have.
		wantToolChoice model.ToolChoice
	}{
		{
			name: "triage asks for a full investigation with a strategy",
			turn: triageTurn(`{"investigate": true, "strategy": "latency-alert", "reason": "A p99 alert."}`),
			caps: model.Capabilities{ToolCalling: true, ToolChoice: true},
			want: triageDecision{investigate: true, strategy: "latency-alert", reason: "A p99 alert.", callID: "t1"},
			wantToolChoice: model.ToolChoice{
				Mode: model.ToolChoiceRequired, Name: prompt.SubmitTriageTool,
			},
		},
		{
			name: "triage skips the investigation as a duplicate",
			turn: triageTurn(`{"investigate": false, "strategy": "generic-alert", "reason": "Same alert as INC-12.",
				"verdict": "duplicate", "confidence": "high"}`),
			caps: model.Capabilities{ToolCalling: true},
			want: triageDecision{
				strategy: "generic-alert", verdict: report.Duplicate, confidence: report.High, reason: "Same alert as INC-12.", callID: "t1",
			},
		},
		{
			name: "a verdict triage may not give",
			turn: triageTurn(`{"investigate": false, "strategy": "generic-alert", "reason": "Clearly broken.",
				"verdict": "true_positive", "confidence": "high"}`),
			caps: model.Capabilities{ToolCalling: true},
			want: triageDecision{callID: "t1", problems: []string{
				`triage may skip the investigation only with duplicate, known_issue or flapping, not "true_positive"`,
			}},
		},
		{
			name: "an unknown strategy name",
			turn: triageTurn(`{"investigate": true, "strategy": "disk-full", "reason": "A disk alert."}`),
			caps: model.Capabilities{ToolCalling: true},
			want: triageDecision{callID: "t1", problems: []string{`strategy "disk-full" is not one of the strategies`}},
		},
		{
			name: "skipping without a confidence",
			turn: triageTurn(`{"investigate": false, "strategy": "generic-alert", "reason": "Flaps.", "verdict": "flapping"}`),
			caps: model.Capabilities{ToolCalling: true},
			want: triageDecision{callID: "t1", problems: []string{"skipping the investigation needs a confidence: low, medium or high"}},
		},
		{
			name: "input that does not match the schema",
			turn: triageTurn(`{"strategy": "generic-alert"}`),
			caps: model.Capabilities{ToolCalling: true},
			want: triageDecision{callID: "t1", problems: []string{"input does not match the tool's schema"}},
		},
		{
			name: "no call to submit_triage",
			turn: modeltest.Text(triageUsage, "Investigate it."),
			caps: model.Capabilities{ToolCalling: true},
			want: unusable("the triage model did not call submit_triage"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := modeltest.New(t, tt.caps, tt.turn)
			conn.ExpectAllUsed()

			resp, got, err := tr.ask(t.Context(), Connector{Connector: conn}, model.Request{}, "payments")
			require.NoError(t, err)
			require.Equal(t, triageUsage, resp.Usage)

			if len(tt.want.problems) > 0 && len(got.problems) > 0 {
				require.Contains(t, got.problems[0], tt.want.problems[0])
				got.problems = tt.want.problems
			}

			require.Equal(t, tt.want, got)

			req := conn.Requests()[0]
			require.Equal(t, tt.wantToolChoice, req.ToolChoice)
			require.Equal(t, map[string]string{model.MetadataRole: string(model.RoleTriage), model.MetadataRoute: "payments"}, req.Metadata)
		})
	}
}

func TestTriagerAskFails(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  *model.Error
	}{
		{name: "a refused request", err: &model.Error{Kind: model.ErrInvalid, Err: errors.New("bad request")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, modeltest.Fail(tt.err))
			conn.ExpectAllUsed()

			_, _, err := newTestTriager(t).ask(t.Context(), Connector{Connector: conn}, model.Request{}, "payments")
			require.ErrorIs(t, err, tt.err)
		})
	}
}

func TestNewHistoryWithTriage(t *testing.T) {
	t.Parallel()

	triageReq, err := encodePrompt(model.Request{System: []model.Text{{Text: "triage"}}, Messages: []model.Message{{Role: model.User}}})
	require.NoError(t, err)

	fullReq := model.Request{System: []model.Text{{Text: "investigate"}}, Messages: []model.Message{{Role: model.User, Parts: []model.Part{model.Text{Text: "event"}}}}}

	full, err := encodePrompt(fullReq)
	require.NoError(t, err)

	answer := triageTurn(`{"investigate":true,"strategy":"latency-alert","reason":"r"}`).Response.Message.Parts
	turn := partsStep(t, kindTriageTurn, answer...)
	turn.InputTokens, turn.OutputTokens = new(int64(800)), new(int64(60))

	triageSteps := []db.Step{
		{Kind: kindTriagePrompt, Parts: triageReq, Status: statusOK},
		turn,
		partsStep(t, kindTriageResult, model.Text{Text: "Triage: a full investigation is needed, with strategy latency-alert."}),
	}

	tests := []struct {
		name         string
		steps        []db.Step
		wantPrompted bool
		wantErr      bool
	}{
		{name: "triage steps only", steps: triageSteps},
		{name: "triage steps, then the prompt", steps: append(append([]db.Step(nil), triageSteps...), db.Step{Kind: kindPrompt, Parts: full}), wantPrompted: true},
		{name: "a triage step after the prompt", steps: []db.Step{{Kind: kindPrompt, Parts: full}, turn}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			steps := append([]db.Step(nil), tt.steps...)
			for i := range steps {
				steps[i].Seq = int32(i) //nolint:gosec // A handful of steps.
			}

			h, err := newHistory(steps)
			if tt.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantPrompted, h.prompted())
			require.Equal(t, 1, h.triage.turn)
			require.Equal(t, 2, h.triage.result)
			require.Equal(t, answer, h.triage.answer)
			require.Equal(t, "triage", h.triage.req.System[0].Text)
			require.Equal(t, int64(860), h.tokens(), "triage tokens count toward the token budget")

			if tt.wantPrompted {
				require.Equal(t, fullReq.Messages, h.request().Messages, "the triage steps are not part of the conversation")
			}
		})
	}
}

func TestBudgetTightened(t *testing.T) {
	t.Parallel()

	stored := Budget{MaxTokens: 400_000, MaxToolCalls: 40, MaxWallTime: 10 * time.Minute, MaxCostMicros: 1_000_000}

	tests := []struct {
		name     string
		budget   Budget
		strategy strategy.Budgets
		want     Budget
	}{
		{name: "a strategy without budgets", budget: stored, want: stored},
		{
			name:     "lower limits apply",
			budget:   stored,
			strategy: strategy.Budgets{MaxToolCalls: 30, MaxWallTime: 5 * time.Minute, MaxCostUSD: 0.5},
			want:     Budget{MaxTokens: 400_000, MaxToolCalls: 30, MaxWallTime: 5 * time.Minute, MaxCostMicros: 500_000},
		},
		{
			name:     "higher limits never raise one",
			budget:   stored,
			strategy: strategy.Budgets{MaxTokens: 1_000_000, MaxToolCalls: 80, MaxWallTime: time.Hour, MaxCostUSD: 5},
			want:     stored,
		},
		{
			name:     "a limit the stored budget does not set",
			budget:   Budget{MaxToolCalls: 40},
			strategy: strategy.Budgets{MaxTokens: 100_000},
			want:     Budget{MaxTokens: 100_000, MaxToolCalls: 40},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, tt.budget.tightened(tt.strategy))
		})
	}
}

// newTestTriager returns a triager with the built-in strategies.
func newTestTriager(t *testing.T) triager {
	t.Helper()

	schema, err := tools.ParseInputSchema(submitTriageSchema)
	require.NoError(t, err)

	set, err := strategy.Load(strategies.FS, "", nil)
	require.NoError(t, err)

	return triager{schema: schema, strategies: set}
}

// triageTurn is a triage model turn that calls submit_triage with input.
func triageTurn(input string) modeltest.Turn {
	return modeltest.ToolUse(triageUsage, modeltest.Call("t1", prompt.SubmitTriageTool, input))
}

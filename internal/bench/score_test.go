package bench

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// shareDelta is how far apart two shares may be and still be equal.
const shareDelta = 1e-9

// commitURL is the URL the evidence tests cite.
const commitURL = "https://github.com/acme/checkout/commit/abc123"

// poolRubric is a rubric with two required items and one that is not.
var poolRubric = []RubricItem{
	{ID: "pool", Text: "Names pool exhaustion", Weight: 2, Required: true},
	{ID: "commit", Text: "Links commit abc123", Weight: 2, Required: true},
	{ID: "fix", Text: "Proposes restoring the pool size", Weight: 1},
}

func TestScoreMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scenarios []ScenarioResult
		want      func(t *testing.T, m Metrics)
	}{
		{
			name: "no runs has no shares",
			want: func(t *testing.T, m Metrics) {
				t.Helper()

				require.Equal(t, Metrics{}, m)
			},
		},
		{
			name: "empty classes have no precision or recall",
			scenarios: []ScenarioResult{
				scenario("flaky", report.Flapping, verdictRun(report.Flapping), verdictRun(report.Inconclusive)),
			},
			want: func(t *testing.T, m Metrics) {
				t.Helper()

				requireShare(t, new(0.5), m.VerdictAccuracy)
				require.Equal(t, ClassMetrics{}, m.FalsePositive)
				require.Equal(t, ClassMetrics{}, m.TruePositive)
				require.Nil(t, m.RootCauseMatch)
				require.Nil(t, m.Evidence)
				require.Nil(t, m.SourceFailuresReported)
				requireShare(t, new(0.5), m.Overall)
			},
		},
		{
			name: "precision and recall of each class",
			scenarios: []ScenarioResult{
				scenario("real", report.TruePositive, verdictRun(report.TruePositive), verdictRun(report.FalsePositive), verdictRun(report.TruePositive)),
				scenario("noise", report.FalsePositive, verdictRun(report.FalsePositive), verdictRun(report.TruePositive), verdictRun(report.Inconclusive)),
			},
			want: func(t *testing.T, m Metrics) {
				t.Helper()

				require.Equal(t, 6, m.Runs)
				requireShare(t, new(0.5), m.VerdictAccuracy)
				require.Equal(t, 2, m.FalsePositive.Predicted)
				require.Equal(t, 3, m.FalsePositive.Expected)
				require.Equal(t, 1, m.FalsePositive.Hits)
				requireShare(t, new(1.0/2), m.FalsePositive.Precision)
				requireShare(t, new(1.0/3), m.FalsePositive.Recall)
				require.Equal(t, 3, m.TruePositive.Predicted)
				require.Equal(t, 3, m.TruePositive.Expected)
				require.Equal(t, 2, m.TruePositive.Hits)
				requireShare(t, new(2.0/3), m.TruePositive.Precision)
				requireShare(t, new(2.0/3), m.TruePositive.Recall)
			},
		},
		{
			name: "acceptable verdicts count as correct",
			scenarios: []ScenarioResult{{
				ID:       "either",
				Expected: Expected{Verdict: report.Inconclusive, Acceptable: []report.Verdict{report.Flapping}},
				Runs:     []RunResult{verdictRun(report.Flapping), verdictRun(report.TruePositive)},
			}},
			want: func(t *testing.T, m Metrics) {
				t.Helper()

				requireShare(t, new(0.5), m.VerdictAccuracy)
			},
		},
		{
			name: "evidence checks in code",
			scenarios: []ScenarioResult{{
				ID: "evidence",
				Expected: Expected{Verdict: report.TruePositive, Evidence: []EvidenceCheck{
					{CalledTool: "github__list_commits"}, {CitedURL: commitURL},
				}},
				Runs: []RunResult{
					withCitation(withTool(verdictRun(report.TruePositive), "github__list_commits"), commitURL),
					withTool(verdictRun(report.TruePositive), "github__list_commits"),
					withCitation(withTool(verdictRun(report.TruePositive), "github__get_commit"), "https://example.com/other"),
				},
			}},
			want: func(t *testing.T, m Metrics) {
				t.Helper()

				requireShare(t, new(0.5), m.Evidence)
				// Verdicts 1.0 with weight 0.5 and evidence 0.5 with weight
				// 0.2; the root cause has no value and is left out.
				requireShare(t, new((0.5*1+0.2*0.5)/0.7), m.Overall)
			},
		},
		{
			name: "source failures reported",
			scenarios: []ScenarioResult{{
				ID:       "k8s-down",
				Expected: Expected{Verdict: report.Inconclusive, UnavailableReported: []string{"k8s"}},
				Runs: []RunResult{
					withCheck(verdictRun(report.Inconclusive), "k8s", report.CheckUnavailable),
					withCheck(verdictRun(report.Inconclusive), "k8s", report.CheckFailed),
				},
			}},
			want: func(t *testing.T, m Metrics) {
				t.Helper()

				requireShare(t, new(0.5), m.SourceFailuresReported)
			},
		},
		{
			name: "cost, tokens, tool calls and time",
			scenarios: []ScenarioResult{scenario("cost", report.TruePositive,
				usageRun(1000, time.Second), usageRun(2000, 3*time.Second), usageRun(3000, 2*time.Second))},
			want: func(t *testing.T, m Metrics) {
				t.Helper()

				require.Equal(t, int64(6000), m.CostMicros)
				require.Equal(t, 6, m.ToolCalls)
				require.Equal(t, int64(300), m.InputTokens)
				require.Equal(t, int64(30), m.OutputTokens)
				requireShare(t, new(0.002), m.MeanCostPerRunUSD)
				requireShare(t, new(2000.0), m.MeanDurationMs)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results := &Results{Scenarios: tt.scenarios}
			require.NoError(t, Score(t.Context(), results, ScoreOptions{Seed: DefaultSeed}))
			require.NotNil(t, results.Summary)
			require.Equal(t, DefaultWeights, results.Summary.Weights)
			tt.want(t, results.Summary.Metrics)
		})
	}
}

func TestScoreRootCause(t *testing.T) {
	t.Parallel()

	allMet := Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "commit", Met: true}, {ID: "fix", Met: true}}, CostMicros: 100}
	noCommit := Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "commit", Reason: "no commit"}, {ID: "fix", Met: true}}, CostMicros: 100}
	noFix := Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "commit", Met: true}}, CostMicros: 100}

	tests := []struct {
		name       string
		runs       []RunResult
		setupMocks func(g *MockGrader)
		wantErr    error
		want       func(t *testing.T, results *Results)
	}{
		{
			name: "met weight over total weight, and a match needs the required items",
			runs: []RunResult{summaryRun("all"), summaryRun("no commit"), summaryRun("no fix")},
			setupMocks: func(g *MockGrader) {
				gomock.InOrder(
					g.EXPECT().Grade(gomock.Any(), RootCause{Rubric: poolRubric}, summaryRun("all").Report).Return(allMet, nil),
					g.EXPECT().Grade(gomock.Any(), RootCause{Rubric: poolRubric}, summaryRun("no commit").Report).Return(noCommit, nil),
					g.EXPECT().Grade(gomock.Any(), RootCause{Rubric: poolRubric}, summaryRun("no fix").Report).Return(noFix, nil),
				)
			},
			want: func(t *testing.T, results *Results) {
				t.Helper()

				runs := results.Scenarios[0].Runs
				require.InDelta(t, 1.0, runs[0].RootCause.Score, shareDelta)
				require.True(t, runs[0].RootCause.Match)
				require.InDelta(t, 3.0/5, runs[1].RootCause.Score, shareDelta)
				require.False(t, runs[1].RootCause.RequiredMet)
				require.False(t, runs[1].RootCause.Match)
				require.InDelta(t, 4.0/5, runs[2].RootCause.Score, shareDelta)
				require.True(t, runs[2].RootCause.Match)
				require.Equal(t, "The judge did not grade this item.", runs[2].RootCause.Items[2].Reason)

				m := results.Summary.Metrics
				requireShare(t, new(2.0/3), m.RootCauseMatch)
				requireShare(t, new(0.8), m.RootCauseScore)
				require.Equal(t, int64(300), m.JudgeCostMicros)

				spread := results.Scenarios[0].Spread
				requireShare(t, new(0.8), spread.RootCauseMean)
				requireShare(t, new(0.2), spread.RootCauseSD)
			},
		},
		{
			name: "a run without a report is not sent to the judge",
			runs: []RunResult{{Run: 1, Status: "failed"}},
			want: func(t *testing.T, results *Results) {
				t.Helper()

				rc := results.Scenarios[0].Runs[0].RootCause
				require.NotNil(t, rc)
				require.Zero(t, rc.Score)
				require.False(t, rc.Match)
				require.Equal(t, noReportReason, rc.Items[0].Reason)
			},
		},
		{
			name: "a judge that gives no grades is kept on the run",
			runs: []RunResult{summaryRun("all")},
			setupMocks: func(g *MockGrader) {
				g.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).Return(Grade{}, errors.New("no grades after 3 attempts"))
			},
			want: func(t *testing.T, results *Results) {
				t.Helper()

				rc := results.Scenarios[0].Runs[0].RootCause
				require.Equal(t, "no grades after 3 attempts", rc.Error)
				require.Equal(t, 1, results.Summary.Metrics.JudgeErrors)
				requireShare(t, new(0.0), results.Summary.Metrics.RootCauseMatch)
			},
		},
		{
			name: "an unreachable judge stops scoring",
			runs: []RunResult{summaryRun("all")},
			setupMocks: func(g *MockGrader) {
				g.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).Return(Grade{}, ErrProviderUnreachable)
			},
			wantErr: ErrInfrastructure,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grader := NewMockGrader(gomock.NewController(t))
			if tt.setupMocks != nil {
				tt.setupMocks(grader)
			}

			results := &Results{Scenarios: []ScenarioResult{{
				ID:       "pool",
				Expected: Expected{Verdict: report.TruePositive, RootCause: RootCause{Rubric: poolRubric}},
				Runs:     tt.runs,
			}}}

			err := Score(t.Context(), results, ScoreOptions{Grader: grader, Seed: DefaultSeed})
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)

				return
			}

			require.NoError(t, err)
			tt.want(t, results)
		})
	}
}

func TestScoreStopsWhenContextEnds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
	}{
		{name: "a grading cut short by the context ending stops scoring"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			grader := NewMockGrader(gomock.NewController(t))
			grader.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).DoAndReturn(
				func(context.Context, RootCause, report.Report) (Grade, error) {
					cancel()

					return Grade{}, context.Canceled
				})

			results := &Results{Scenarios: []ScenarioResult{{
				ID:       "pool",
				Expected: Expected{Verdict: report.TruePositive, RootCause: RootCause{Rubric: poolRubric}},
				Runs:     []RunResult{summaryRun("all")},
			}}}

			require.ErrorIs(t, Score(ctx, results, ScoreOptions{Grader: grader}), context.Canceled)
		})
	}
}

func TestSpread(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		runs []RunResult
		want Spread
	}{
		{
			name: "no runs",
			want: Spread{},
		},
		{
			name: "an odd number of runs",
			runs: []RunResult{usageRun(1000, time.Second), usageRun(3000, 3*time.Second), usageRun(2000, 2*time.Second)},
			want: Spread{
				Runs: 3, Correct: 3,
				CostUSD:    Range{Min: 0.001, Median: 0.002, Max: 0.003},
				DurationMs: Range{Min: 1000, Median: 2000, Max: 3000},
			},
		},
		{
			name: "an even number of runs takes the mean of the middle two",
			runs: []RunResult{usageRun(1000, time.Second), usageRun(4000, 4*time.Second)},
			want: Spread{
				Runs: 2, Correct: 2,
				CostUSD:    Range{Min: 0.001, Median: 0.0025, Max: 0.004},
				DurationMs: Range{Min: 1000, Median: 2500, Max: 4000},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			results := &Results{Scenarios: []ScenarioResult{scenario("spread", report.TruePositive, tt.runs...)}}
			require.NoError(t, Score(t.Context(), results, ScoreOptions{}))

			got := *results.Scenarios[0].Spread
			require.Equal(t, tt.want.Runs, got.Runs)
			require.Equal(t, tt.want.Correct, got.Correct)
			require.Nil(t, got.RootCauseMean)
			requireRange(t, tt.want.CostUSD, got.CostUSD)
			requireRange(t, tt.want.DurationMs, got.DurationMs)
		})
	}
}

func TestScoreWeights(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		weights Weights
		want    float64
	}{
		{name: "the default weights", want: (0.5*0.5 + 0.3*1 + 0.2*0) / 1},
		{name: "verdicts only", weights: Weights{Verdict: 1}, want: 0.5},
		{name: "root cause and evidence", weights: Weights{RootCause: 1, Evidence: 1}, want: 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			grader := NewMockGrader(gomock.NewController(t))
			grader.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).
				Return(Grade{Items: []ItemGrade{{ID: "pool", Met: true}, {ID: "commit", Met: true}, {ID: "fix", Met: true}}}, nil).Times(2)

			// Half the verdicts are right, every root cause matches and no
			// evidence check passes.
			results := &Results{Scenarios: []ScenarioResult{{
				ID: "weights",
				Expected: Expected{
					Verdict:   report.TruePositive,
					RootCause: RootCause{Rubric: poolRubric},
					Evidence:  []EvidenceCheck{{CalledTool: "github__list_commits"}},
				},
				Runs: []RunResult{summaryRun("a"), withVerdict(summaryRun("b"), report.Inconclusive)},
			}}}

			require.NoError(t, Score(t.Context(), results, ScoreOptions{Grader: grader, Weights: tt.weights}))
			requireShare(t, new(tt.want), results.Summary.Metrics.Overall)
		})
	}
}

// scenario returns a scenario result that expects verdict, with runs.
func scenario(id string, verdict report.Verdict, runs ...RunResult) ScenarioResult {
	for i := range runs {
		runs[i].Run = i + 1
	}

	return ScenarioResult{ID: id, Expected: Expected{Verdict: verdict}, Runs: runs}
}

// verdictRun returns a finished run that gave verdict.
func verdictRun(verdict report.Verdict) RunResult {
	return RunResult{Run: 1, Status: "final", Report: report.Report{Verdict: verdict, StopReason: report.StopSubmitted}}
}

// summaryRun returns a true positive run whose report has summary.
func summaryRun(summary string) RunResult {
	run := verdictRun(report.TruePositive)
	run.Report.Summary = summary

	return run
}

// usageRun returns a true positive run that cost costMicros and took took,
// with 2 tool calls, 100 input tokens and 10 output tokens.
func usageRun(costMicros int64, took time.Duration) RunResult {
	run := verdictRun(report.TruePositive)
	run.CostMicros = costMicros
	run.Duration = took
	run.ToolCalls = 2
	run.InputTokens = 100
	run.OutputTokens = 10

	return run
}

// withVerdict returns run with verdict.
func withVerdict(run RunResult, verdict report.Verdict) RunResult {
	run.Report.Verdict = verdict

	return run
}

// withTool returns run with a call to tool.
func withTool(run RunResult, tool string) RunResult {
	run.ToolResults = append(run.ToolResults, ToolResult{Tool: tool, Status: "ok"})

	return run
}

// withCitation returns run with a finding that cites url.
func withCitation(run RunResult, url string) RunResult {
	run.Report.Findings = append(run.Report.Findings, report.Finding{
		ID: "f1", Claim: "a claim", Evidence: []source.Evidence{{Kind: source.EvidenceCommit, Title: "abc123", URL: url}},
	})

	return run
}

// withCheck returns run with a checked source.
func withCheck(run RunResult, name string, status report.CheckStatus) RunResult {
	run.Report.Checked = append(run.Report.Checked, report.Check{Source: name, Status: status})

	return run
}

// requireShare checks that got is nil when want is, and otherwise holds
// want, give or take shareDelta.
func requireShare(t *testing.T, want, got *float64) {
	t.Helper()

	if want == nil {
		require.Nil(t, got)

		return
	}

	require.NotNil(t, got)
	require.InDelta(t, *want, *got, shareDelta)
}

// requireRange checks each value of got against want, give or take
// shareDelta.
func requireRange(t *testing.T, want, got Range) {
	t.Helper()

	require.InDelta(t, want.Min, got.Min, shareDelta)
	require.InDelta(t, want.Median, got.Median, shareDelta)
	require.InDelta(t, want.Max, got.Max, shareDelta)
}

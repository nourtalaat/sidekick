package bench

import (
	"bytes"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/nourtalaat/sidekick/internal/model"
	"github.com/nourtalaat/sidekick/internal/model/modeltest"
	"github.com/nourtalaat/sidekick/internal/report"
)

// retryWait is the wait the provider asks for in the tests, which replaces
// the backoff, so the tests do not sleep long.
const retryWait = time.Millisecond

// allMetGrades is a submit_grades call that meets every item of poolRubric.
const allMetGrades = `{"grades":[` +
	`{"id":"pool","met":true,"reason":"named"},` +
	`{"id":"commit","met":true,"reason":"linked"},` +
	`{"id":"fix","met":true,"reason":"proposed"}]}`

func TestJudgeRetries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		retries int
		turns   []modeltest.Turn
		// wantRequests is how many times the judge called the model.
		wantRequests int
		wantErr      error
		wantErrText  string
		wantMet      int
	}{
		{
			name:         "succeeds after one overloaded answer",
			retries:      1,
			turns:        []modeltest.Turn{modeltest.Fail(overloaded()), judgeGrades()},
			wantRequests: 2,
			wantMet:      3,
		},
		{
			name:         "succeeds after a rate limit and a timeout",
			retries:      2,
			turns:        []modeltest.Turn{modeltest.Fail(modelError(model.ErrRateLimited)), modeltest.Fail(modelError(model.ErrTimeout)), judgeGrades()},
			wantRequests: 3,
			wantMet:      3,
		},
		{
			name:         "fails when every attempt is overloaded",
			retries:      2,
			turns:        []modeltest.Turn{modeltest.Fail(overloaded()), modeltest.Fail(overloaded()), modeltest.Fail(overloaded())},
			wantRequests: 3,
			wantErr:      ErrProviderUnreachable,
			wantErrText:  "attempt 3 of 3",
		},
		{
			name:         "does not retry without retries",
			retries:      0,
			turns:        []modeltest.Turn{modeltest.Fail(overloaded())},
			wantRequests: 1,
			wantErr:      ErrProviderUnreachable,
			wantErrText:  "attempt 1 of 1",
		},
		{
			name:         "does not retry a request the provider refuses",
			retries:      3,
			turns:        []modeltest.Turn{modeltest.Fail(modelError(model.ErrInvalid))},
			wantRequests: 1,
			wantErrText:  "the judge model refused the request",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, tt.turns...)
			conn.ExpectAllUsed()

			judge, err := NewJudge(JudgeOptions{Connector: conn, Retries: tt.retries, Name: "judge"})
			require.NoError(t, err)

			got, err := judge.Grade(t.Context(), RootCause{Rubric: poolRubric}, gradedPool)
			require.Len(t, conn.Requests(), tt.wantRequests)

			if tt.wantErrText != "" || tt.wantErr != nil {
				require.Error(t, err)

				if tt.wantErr != nil {
					require.ErrorIs(t, err, tt.wantErr)
				} else {
					require.NotErrorIs(t, err, ErrInfrastructure)
				}

				require.ErrorContains(t, err, tt.wantErrText)

				return
			}

			require.NoError(t, err)

			met := 0

			for _, item := range got.Items {
				if item.Met {
					met++
				}
			}

			require.Equal(t, tt.wantMet, met)
		})
	}
}

func TestScoreKeepsAJudgeThatStaysDown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		// failing says, for each run in order, whether the judge is down for it.
		failing []bool
		// keep sets ScoreOptions.KeepInfrastructureErrors.
		keep          bool
		wantScoreErr  error
		wantJudgeErrs int
		wantRequests  int
	}{
		{
			name:          "one run the judge cannot grade, the others are scored",
			failing:       []bool{true, false, false},
			keep:          true,
			wantJudgeErrs: 1,
			wantRequests:  2 + 1 + 1,
		},
		{
			name:          "a judge that is down for every run",
			failing:       []bool{true, true, true},
			keep:          true,
			wantJudgeErrs: 3,
			wantRequests:  6,
		},
		{
			name:         "without the option the failure stops scoring",
			failing:      []bool{true, false, false},
			keep:         false,
			wantScoreErr: ErrProviderUnreachable,
			wantRequests: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// Each run gets 2 attempts, from the judge's one retry.
			var turns []modeltest.Turn

			for _, down := range tt.failing {
				if down {
					turns = append(turns, modeltest.Fail(overloaded()), modeltest.Fail(overloaded()))

					continue
				}

				turns = append(turns, judgeGrades())
			}

			conn := modeltest.New(t, model.Capabilities{ToolCalling: true}, turns...)

			judge, err := NewJudge(JudgeOptions{Connector: conn, Retries: 1, Name: "judge"})
			require.NoError(t, err)

			runs := make([]RunResult, len(tt.failing))
			for i := range runs {
				runs[i] = summaryRun("run")
				runs[i].Run = i + 1
			}

			results := &Results{Repeat: len(runs), Scenarios: []ScenarioResult{{
				ID:       "pool",
				Expected: Expected{Verdict: report.TruePositive, RootCause: RootCause{Rubric: poolRubric}},
				Runs:     runs,
			}}}

			err = Score(t.Context(), results, ScoreOptions{Grader: judge, Seed: DefaultSeed, KeepInfrastructureErrors: tt.keep})
			require.Len(t, conn.Requests(), tt.wantRequests)

			if tt.wantScoreErr != nil {
				require.ErrorIs(t, err, tt.wantScoreErr)

				return
			}

			require.NoError(t, err)
			require.Equal(t, tt.wantJudgeErrs, results.Summary.Metrics.JudgeErrors)

			graded := 0

			for i, run := range results.Scenarios[0].Runs {
				require.NotNil(t, run.RootCause)

				if tt.failing[i] {
					requireJudgeError(t, run.RootCause)

					continue
				}

				graded++

				require.Empty(t, run.RootCause.Error)
				require.True(t, run.RootCause.Match)
			}

			require.Equal(t, len(tt.failing)-tt.wantJudgeErrs, graded)
			requireOutputsWritten(t, results, tt.wantJudgeErrs)
		})
	}
}

// requireJudgeError checks that rc reads as a judge error and not as a real
// root cause score of 0: it has an error, and no score, grades or match.
func requireJudgeError(t *testing.T, rc *RootCauseResult) {
	t.Helper()

	require.Contains(t, rc.Error, "attempt 2 of 2")
	require.Contains(t, rc.Error, ErrProviderUnreachable.Error())
	require.Zero(t, rc.Score)
	require.False(t, rc.Match)
	require.False(t, rc.RequiredMet)
	require.Empty(t, rc.Items)
}

// requireOutputsWritten renders report.json, junit.xml and the Markdown
// summary of results, and checks they show wantJudgeErrs judge errors.
func requireOutputsWritten(t *testing.T, results *Results, wantJudgeErrs int) {
	t.Helper()

	rep, err := results.Report(testInfo, nil)
	require.NoError(t, err)
	require.Equal(t, wantJudgeErrs, rep.Metrics.JudgeErrors)

	withError := 0

	for _, run := range rep.Scenarios[0].Runs {
		if run.RootCause.Error != "" {
			withError++

			require.Zero(t, run.RootCause.Score)
			require.False(t, run.RootCause.Match)
		}
	}

	require.Equal(t, wantJudgeErrs, withError)

	var js, junit, md bytes.Buffer

	require.NoError(t, rep.WriteJSON(&js))
	require.NoError(t, results.WriteJUnit(&junit, nil))
	require.NoError(t, results.WriteMarkdown(&md, testInfo, nil))
	require.Contains(t, md.String(), "| judge errors | "+strconv.Itoa(wantJudgeErrs)+" | |")

	if wantJudgeErrs > 0 {
		require.Contains(t, js.String(), `"error"`)
		require.Contains(t, junit.String(), "the judge gave no grades")
	}
}

// overloaded returns an overloaded provider error that asks for a short wait.
func overloaded() error {
	return &model.Error{Kind: model.ErrOverloaded, RetryAfter: retryWait, Err: errors.New("503 Service Unavailable")}
}

// modelError returns a provider error of kind that asks for a short wait.
func modelError(kind model.ErrorKind) error {
	return &model.Error{Kind: kind, RetryAfter: retryWait, Err: errors.New("provider error")}
}

// judgeGrades returns a turn in which the judge meets every item of poolRubric.
func judgeGrades() modeltest.Turn {
	return modeltest.ToolUse(model.Usage{InputTokens: 100, OutputTokens: 10}, modeltest.Call("toolu_grades", SubmitGradesTool, allMetGrades))
}

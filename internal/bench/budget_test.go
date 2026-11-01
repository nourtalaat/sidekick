package bench

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/nourtalaat/sidekick/internal/report"
)

func TestBudgetCheckNext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		max     int64
		runs    []int64
		other   int64
		wantErr bool
	}{
		{name: "nothing spent is within any cap", max: 1000},
		{name: "no cap allows any amount", max: 0, runs: []int64{1_000_000, 2_000_000}},
		{name: "a negative cap is no cap", max: -1, runs: []int64{5}},
		{name: "room for one more run at the mean", max: 1300, runs: []int64{300, 300, 300}},
		{name: "exactly the cap after one more run", max: 1200, runs: []int64{300, 300, 300}},
		{name: "one more run at the mean would pass the cap", max: 1100, runs: []int64{300, 300, 300}, wantErr: true},
		{name: "the mean is of the runs, not of the grading", max: 1000, runs: []int64{100, 100}, other: 600},
		{name: "grading counts in what is spent", max: 1000, runs: []int64{100, 100}, other: 750, wantErr: true},
		{name: "the first run has no mean, so it is allowed", max: 10},
		{name: "already past the cap", max: 100, runs: []int64{500}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := NewBudget(tt.max)
			for _, cost := range tt.runs {
				b.AddRun(cost)
			}

			b.AddOther(tt.other)

			err := b.CheckNext()
			if tt.wantErr {
				require.ErrorIs(t, err, ErrOverBudget)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestBudgetCheckSpent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		max     int64
		spent   int64
		wantErr bool
	}{
		{name: "under the cap", max: 100, spent: 99},
		{name: "at the cap", max: 100, spent: 100, wantErr: true},
		{name: "over the cap", max: 100, spent: 150, wantErr: true},
		{name: "no cap", max: 0, spent: 1_000_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			b := NewBudget(tt.max)
			b.AddOther(tt.spent)

			require.Equal(t, tt.spent, b.Spent())

			if tt.wantErr {
				require.ErrorIs(t, b.CheckSpent(), ErrOverBudget)

				return
			}

			require.NoError(t, b.CheckSpent())
		})
	}
}

func TestNilBudgetCapsNothing(t *testing.T) {
	t.Parallel()

	var b *Budget

	b.AddRun(5)
	b.AddOther(5)

	require.NoError(t, b.CheckNext())
	require.NoError(t, b.CheckSpent())
	require.Zero(t, b.Spent())
}

func TestBudgetGrader(t *testing.T) {
	t.Parallel()

	const cost = 400

	tests := []struct {
		name      string
		max       int64
		wantCalls int
		wantErr   bool
	}{
		{name: "a grading under the cap is allowed", max: 500, wantCalls: 2},
		{name: "a grading that follows one that spent the cap is refused", max: 400, wantCalls: 1, wantErr: true},
		{name: "no cap grades every time", max: 0, wantCalls: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			inner := NewMockGrader(gomock.NewController(t))
			inner.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).Return(Grade{CostMicros: cost}, nil).Times(tt.wantCalls)

			budget := NewBudget(tt.max)
			grader := NewBudgetGrader(inner, budget)

			_, err := grader.Grade(t.Context(), RootCause{}, report.Report{})
			require.NoError(t, err)
			require.Equal(t, int64(cost), budget.Spent())

			_, err = grader.Grade(t.Context(), RootCause{}, report.Report{})
			if tt.wantErr {
				require.ErrorIs(t, err, ErrOverBudget)

				return
			}

			require.NoError(t, err)
		})
	}
}

func TestNewBudgetGraderWithoutABudgetIsTheInnerGrader(t *testing.T) {
	t.Parallel()

	inner := NewMockGrader(gomock.NewController(t))

	require.Same(t, inner, NewBudgetGrader(inner, nil))
}

func TestScoreStopsWhenTheBudgetIsSpent(t *testing.T) {
	t.Parallel()

	inner := NewMockGrader(gomock.NewController(t))
	inner.EXPECT().Grade(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(context.Context, RootCause, report.Report) (Grade, error) {
			return Grade{Items: []ItemGrade{{ID: "pool", Met: true}}, CostMicros: 500}, nil
		}).Times(1)

	results := &Results{Scenarios: []ScenarioResult{{
		ID:       "pool",
		Expected: Expected{Verdict: report.TruePositive, RootCause: RootCause{Rubric: poolRubric}},
		Runs:     []RunResult{summaryRun("a"), summaryRun("b")},
	}}}

	err := Score(t.Context(), results, ScoreOptions{Grader: NewBudgetGrader(inner, NewBudget(500))})

	require.ErrorIs(t, err, ErrOverBudget)
}

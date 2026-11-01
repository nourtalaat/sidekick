package bench

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// gateSeed is the seed of the tests, so every interval is the same on every
// run.
const gateSeed = 7

// armOf returns the scored scenarios of one arm: scenario i has runs runs, of
// which correct[i] have a correct verdict. With no evidence checks and no
// rubric, the overall score of a scenario is its share of correct runs.
func armOf(runs int, correct ...int) []ScenarioResult {
	out := make([]ScenarioResult, len(correct))

	for i, k := range correct {
		sc := ScenarioResult{ID: fmt.Sprintf("scenario-%02d", i)}

		for r := range runs {
			sc.Runs = append(sc.Runs, RunResult{Run: r + 1, Scored: true, VerdictCorrect: r < k})
		}

		out[i] = sc
	}

	return out
}

func TestDecideGate(t *testing.T) {
	t.Parallel()

	const runs = 5

	tests := []struct {
		name      string
		baseline  []ScenarioResult
		proposal  []ScenarioResult
		maxDrop   float64
		want      GateResult
		wantCount int
	}{
		{
			name:      "a clear improvement passes",
			baseline:  armOf(runs, 2, 2, 3, 2, 3, 2),
			proposal:  armOf(runs, 5, 4, 5, 4, 5, 4),
			maxDrop:   0.02,
			want:      GatePassed,
			wantCount: 6,
		},
		{
			name:      "identical results pass",
			baseline:  armOf(runs, 5, 3, 4, 2),
			proposal:  armOf(runs, 5, 3, 4, 2),
			maxDrop:   0.02,
			want:      GatePassed,
			wantCount: 4,
		},
		{
			name:      "a clear drop on every scenario fails",
			baseline:  armOf(runs, 5, 5, 5, 4, 5),
			proposal:  armOf(runs, 2, 1, 2, 1, 2),
			maxDrop:   0.02,
			want:      GateFailed,
			wantCount: 5,
		},
		{
			name:      "no change on average with a wide spread is not enough evidence",
			baseline:  armOf(runs, 0, 5, 0, 5, 3),
			proposal:  armOf(runs, 5, 0, 5, 0, 3),
			maxDrop:   0.02,
			want:      GateNotEnoughEvidence,
			wantCount: 5,
		},
		{
			name:      "a small drop that the spread cannot rule out is not enough evidence",
			baseline:  armOf(runs, 5, 5, 5, 5),
			proposal:  armOf(runs, 5, 5, 5, 4),
			maxDrop:   0.1,
			want:      GateNotEnoughEvidence,
			wantCount: 4,
		},
		{
			name:      "a drop below the allowed one on average fails even with a wide spread",
			baseline:  armOf(runs, 5, 5, 5, 5),
			proposal:  armOf(runs, 0, 5, 5, 3),
			maxDrop:   0.02,
			want:      GateFailed,
			wantCount: 4,
		},
		{
			name:      "two scenarios are too few",
			baseline:  armOf(runs, 5, 5),
			proposal:  armOf(runs, 5, 5),
			maxDrop:   0.02,
			want:      GateNotEnoughEvidence,
			wantCount: 2,
		},
		{
			name:      "one scenario is too few",
			baseline:  armOf(runs, 3),
			proposal:  armOf(runs, 5),
			maxDrop:   0.02,
			want:      GateNotEnoughEvidence,
			wantCount: 1,
		},
		{
			name:      "no scenario means no benchmark coverage",
			maxDrop:   0.02,
			want:      GateNoBenchmarkCoverage,
			wantCount: 0,
		},
		{
			name:      "scenarios that only one arm ran do not count",
			baseline:  armOf(runs, 5, 5, 5, 5),
			proposal:  armOf(runs, 5),
			maxDrop:   0.02,
			want:      GateNotEnoughEvidence,
			wantCount: 1,
		},
		{
			name:      "scenarios without runs do not count",
			baseline:  append(armOf(runs, 5, 5, 5), ScenarioResult{ID: "scenario-99"}),
			proposal:  append(armOf(runs, 5, 5, 5), ScenarioResult{ID: "scenario-99"}),
			maxDrop:   0.02,
			want:      GatePassed,
			wantCount: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := DecideGate(tt.baseline, tt.proposal, GateOptions{MaxDrop: tt.maxDrop, Seed: gateSeed})

			require.Equal(t, tt.want, got.Result, got.Reason)
			require.Len(t, got.Scenarios, tt.wantCount)
			require.NotEmpty(t, got.Reason)
			require.Equal(t, GateResamples, got.Resamples)
			require.Equal(t, uint64(gateSeed), got.Seed)
		})
	}
}

func TestDecideGateIntervalOfEqualDifferences(t *testing.T) {
	t.Parallel()

	// Every scenario moves by the same 0.4, so every resample has the same
	// mean and the interval is that one point.
	got := DecideGate(armOf(5, 1, 1, 1, 1), armOf(5, 3, 3, 3, 3), GateOptions{MaxDrop: 0.02, Seed: gateSeed})

	require.InDelta(t, 0.4, got.Mean, 1e-9)
	require.InDelta(t, 0.4, got.Interval.Lower, 1e-9)
	require.InDelta(t, 0.4, got.Interval.Upper, 1e-9)
}

func TestDecideGateIntervalHoldsTheMean(t *testing.T) {
	t.Parallel()

	got := DecideGate(armOf(5, 0, 5, 1, 4, 2, 3, 5), armOf(5, 1, 5, 0, 5, 3, 3, 4), GateOptions{MaxDrop: 0.02, Seed: gateSeed})

	require.Less(t, got.Interval.Lower, got.Mean)
	require.Greater(t, got.Interval.Upper, got.Mean)
	require.Less(t, got.Interval.Lower, got.Interval.Upper)
}

func TestDecideGateSeeds(t *testing.T) {
	t.Parallel()

	baseline, proposal := armOf(5, 0, 5, 1, 4, 2, 3, 5), armOf(5, 1, 5, 0, 5, 3, 3, 4)

	same1 := DecideGate(baseline, proposal, GateOptions{MaxDrop: 0.02, Seed: gateSeed})
	same2 := DecideGate(baseline, proposal, GateOptions{MaxDrop: 0.02, Seed: gateSeed})
	other := DecideGate(baseline, proposal, GateOptions{MaxDrop: 0.02, Seed: gateSeed + 1})

	require.Equal(t, same1, same2, "the same seed gives the same decision")
	require.NotEqual(t, same1.Interval, other.Interval, "another seed resamples differently")
	require.InDelta(t, same1.Interval.Lower, other.Interval.Lower, 0.15, "but both estimate the same interval")
	require.Equal(t, same1.Mean, other.Mean, "the mean does not depend on the seed")
}

func TestDecideGateUsesTheWeights(t *testing.T) {
	t.Parallel()

	// The proposal is worse at root causes and equal at verdicts. With the
	// default weights the loss shows; with only the verdict counting, it
	// does not.
	baseline, proposal := armOf(5, 5, 5, 5, 5), armOf(5, 5, 5, 5, 5)

	for i := range baseline {
		for j := range baseline[i].Runs {
			baseline[i].Runs[j].RootCause = &RootCauseResult{Score: 1, Match: true}
			proposal[i].Runs[j].RootCause = &RootCauseResult{Score: 0}
		}
	}

	byDefault := DecideGate(baseline, proposal, GateOptions{MaxDrop: 0.02, Seed: gateSeed})
	verdictOnly := DecideGate(baseline, proposal, GateOptions{MaxDrop: 0.02, Seed: gateSeed, Weights: Weights{Verdict: 1}})

	require.Equal(t, GateFailed, byDefault.Result)
	require.Equal(t, GatePassed, verdictOnly.Result)
}

func TestJudgeGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		mean    float64
		lower   float64
		maxDrop float64
		want    GateResult
	}{
		{name: "lower end above the limit passes", mean: 0.1, lower: -0.01, maxDrop: 0.02, want: GatePassed},
		{name: "lower end exactly at the limit does not pass", mean: 0, lower: -0.02, maxDrop: 0.02, want: GateNotEnoughEvidence},
		{name: "lower end below the limit with a mean above it", mean: 0.01, lower: -0.2, maxDrop: 0.02, want: GateNotEnoughEvidence},
		{name: "mean exactly at the limit is not a failure", mean: -0.02, lower: -0.2, maxDrop: 0.02, want: GateNotEnoughEvidence},
		{name: "mean below the limit fails", mean: -0.03, lower: -0.2, maxDrop: 0.02, want: GateFailed},
		{name: "a limit of zero needs a lower end above zero", mean: 0.1, lower: 0, maxDrop: 0, want: GateNotEnoughEvidence},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, reason := judgeGate(tt.mean, tt.lower, tt.maxDrop)

			require.Equal(t, tt.want, got)
			require.NotEmpty(t, reason)
		})
	}
}

func TestGateResultPassed(t *testing.T) {
	t.Parallel()

	for _, r := range []GateResult{GateFailed, GateNotEnoughEvidence, GateNoBenchmarkCoverage, GateOverBudget, GateError} {
		require.False(t, r.Passed(), r)
	}

	require.True(t, GatePassed.Passed())
}

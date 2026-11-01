package bench

import (
	"math/rand/v2"
	"slices"
	"strings"

	"github.com/nourtalaat/sidekick/internal/config"
)

const (
	// GateResamples is how many times the scenarios are resampled to find
	// the interval of the strategy gate.
	GateResamples = 10_000
	// MinGateScenarios is the fewest scenarios a gate needs before its
	// interval is trusted. With fewer, resampling shows almost no spread,
	// so the gate ends with GateNotEnoughEvidence and nothing is run.
	MinGateScenarios = 3
	// MinGateRepeats is the fewest times a gate runs each scenario. The
	// config cannot set it lower.
	MinGateRepeats = config.MinGateRepeats
	// DefaultGateMaxDrop is how far below the baseline the lower end of the
	// interval may be when bench.gate.maxDrop is not set.
	DefaultGateMaxDrop = config.DefaultGateMaxDrop
	// DefaultGateMaxCostUSD is the most one gate run may cost when
	// bench.gate.maxCostUSD is not set.
	DefaultGateMaxCostUSD = config.DefaultGateMaxCostUSD
)

// GateResult is how a strategy gate ended.
type GateResult string

// The results of a gate.
const (
	// GatePassed: the proposal is not clearly worse than the baseline.
	GatePassed GateResult = "passed"
	// GateFailed: the proposal is worse on average by more than the drop
	// that is allowed.
	GateFailed GateResult = "failed"
	// GateNotEnoughEvidence: the proposal looks no worse on average, but
	// the interval is too wide to be sure, or too few scenarios cover the
	// strategy. More repeats or more scenarios would help.
	GateNotEnoughEvidence GateResult = "not_enough_evidence"
	// GateNoBenchmarkCoverage: no scenario covers the strategy.
	GateNoBenchmarkCoverage GateResult = "no_benchmark_coverage"
	// GateOverBudget: the gate stopped because it would have cost more than
	// bench.gate.maxCostUSD.
	GateOverBudget GateResult = "over_budget"
	// GateError: the gate could not finish, for example because the model
	// provider was unreachable for good. It is never the result of
	// DecideGate.
	GateError GateResult = "error"
)

// Passed reports whether the result lets a proposal be applied.
func (r GateResult) Passed() bool {
	return r == GatePassed
}

// GateOptions set how DecideGate decides.
type GateOptions struct {
	// MaxDrop is how far below zero the lower end of the interval of the
	// difference may be and still pass. It is the bench.gate.maxDrop
	// setting.
	MaxDrop float64
	// Weights weigh the parts of the overall score. The zero value means
	// DefaultWeights.
	Weights Weights
	// Seed seeds the resampling, so the same results and seed always give
	// the same interval.
	Seed uint64
	// Resamples is how many resamples the interval is computed from. Zero
	// means GateResamples.
	Resamples int
}

// GateScenario is how the two arms of a gate scored one scenario.
type GateScenario struct {
	ID string `json:"id"`
	// Baseline and Proposal are the mean overall score of the scenario's
	// runs in each arm, and Difference is Proposal minus Baseline.
	Baseline   float64 `json:"baseline"`
	Proposal   float64 `json:"proposal"`
	Difference float64 `json:"difference"`
}

// GateDecision is the outcome of a gate: the result, and the numbers it
// rests on, which are stored with the proposal.
type GateDecision struct {
	Result GateResult `json:"result"`
	// Mean is the mean of the scenarios' differences, and Interval its 95%
	// interval from resampling the scenarios.
	Mean     float64  `json:"mean"`
	Interval Interval `json:"interval"`
	MaxDrop  float64  `json:"maxDrop"`
	// Resamples and Seed are what the interval was computed with.
	Resamples int    `json:"resamples"`
	Seed      uint64 `json:"seed"`
	// Scenarios are the scenarios both arms ran, in ID order.
	Scenarios []GateScenario `json:"scenarios"`
	// Reason says in a sentence why the gate ended as it did.
	Reason string `json:"reason"`
}

// DecideGate decides whether a proposal passes. Both arms must be scored.
// For each scenario that both arms ran, it takes the mean overall score of
// the proposal minus that of the baseline, resamples those differences
// with replacement to get a 95% interval for their mean, and passes the
// proposal when the lower end of the interval is above -opts.MaxDrop. That
// is, when the proposal is not clearly worse.
//
// With no scenario in both arms the result is GateNoBenchmarkCoverage, and
// with fewer than MinGateScenarios it is GateNotEnoughEvidence. When the
// interval does not pass, the result is GateFailed if the mean itself is
// below -opts.MaxDrop, and GateNotEnoughEvidence otherwise: the proposal
// looks fine on average, but the spread is too wide to be sure.
func DecideGate(baseline, proposal []ScenarioResult, opts GateOptions) GateDecision {
	weights := opts.Weights
	if weights == (Weights{}) {
		weights = DefaultWeights
	}

	resamples := opts.Resamples
	if resamples <= 0 {
		resamples = GateResamples
	}

	d := GateDecision{MaxDrop: opts.MaxDrop, Resamples: resamples, Seed: opts.Seed, Scenarios: gateScenarios(baseline, proposal, weights)}

	switch n := len(d.Scenarios); {
	case n == 0:
		d.Result = GateNoBenchmarkCoverage
		d.Reason = "No scenario covers the strategy. Export confirmed investigations of it as scenarios."

		return d
	case n < MinGateScenarios:
		d.Result = GateNotEnoughEvidence
		d.Reason = "Too few scenarios cover the strategy to tell a change from chance. Export more confirmed investigations as scenarios."

		return d
	}

	diffs := make([]float64, len(d.Scenarios))
	for i, s := range d.Scenarios {
		diffs[i] = s.Difference
	}

	d.Mean, d.Interval = bootstrapMean(diffs, opts.Seed, resamples)
	d.Result, d.Reason = judgeGate(d.Mean, d.Interval.Lower, opts.MaxDrop)

	return d
}

// judgeGate applies the pass rule to the mean of the differences and the
// lower end of their interval.
func judgeGate(mean, lower, maxDrop float64) (GateResult, string) {
	switch {
	case lower > -maxDrop:
		return GatePassed, "The proposal is not clearly worse than the current version."
	case mean < -maxDrop:
		return GateFailed, "The proposal scores lower than the current version by more than the allowed drop."
	default:
		return GateNotEnoughEvidence, "The proposal looks no worse on average, but the interval is too wide to be sure. Run more repeats or add scenarios."
	}
}

// gateScenarios pairs the scenarios of the two arms by ID and returns the
// scores of those that both arms ran, in ID order. A scenario with no run in
// an arm, or with no score, is left out.
func gateScenarios(baseline, proposal []ScenarioResult, weights Weights) []GateScenario {
	base := overallByID(baseline, weights)
	prop := overallByID(proposal, weights)

	var out []GateScenario

	for id, b := range base {
		p, ok := prop[id]
		if !ok {
			continue
		}

		out = append(out, GateScenario{ID: id, Baseline: b, Proposal: p, Difference: p - b})
	}

	slices.SortFunc(out, func(a, b GateScenario) int { return strings.Compare(a.ID, b.ID) })

	return out
}

// overallByID returns the overall score of each scenario that has runs and a
// score, by scenario ID.
func overallByID(scenarios []ScenarioResult, weights Weights) map[string]float64 {
	out := make(map[string]float64, len(scenarios))

	for _, sc := range scenarios {
		if len(sc.Runs) == 0 {
			continue
		}

		if overall := metricsOf([]ScenarioResult{sc}, weights).Overall; overall != nil {
			out[sc.ID] = *overall
		}
	}

	return out
}

// bootstrapMean returns the mean of values, which must not be empty, and
// the ConfidenceLevel interval of that mean from resampling values with
// replacement resamples times.
func bootstrapMean(values []float64, seed uint64, resamples int) (float64, Interval) {
	rng := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // Resampling needs repeatable, not secret, numbers.
	n := len(values)
	means := make([]float64, resamples)

	for i := range means {
		var sum float64

		for range n {
			sum += values[rng.IntN(n)]
		}

		means[i] = sum / float64(n)
	}

	slices.Sort(means)

	tail := (1 - ConfidenceLevel) / 2

	var total float64
	for _, v := range values {
		total += v
	}

	return total / float64(n), Interval{Lower: quantile(means, tail), Upper: quantile(means, 1-tail)}
}

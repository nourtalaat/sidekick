package bench

import (
	"math"
	"math/rand/v2"
	"slices"
)

const (
	// BootstrapResamples is how many times the scenarios are resampled to
	// find the interval of each run total.
	BootstrapResamples = 1000
	// ConfidenceLevel is the share of the resampled totals each interval
	// holds: the interval runs from the 2.5th to the 97.5th percentile.
	ConfidenceLevel = 0.95
	// DefaultSeed seeds the resampling when no seed is given.
	DefaultSeed = 1
)

// Interval is the range a run total falls in, at ConfidenceLevel, when the
// scenarios are resampled.
type Interval struct {
	Lower float64 `json:"lower"`
	Upper float64 `json:"upper"`
}

// bootstrap resamples scenarios with replacement resamples times, and
// returns the interval of each share metric over the resamples. The random
// numbers come from seed, so the same scenarios and seed always give the
// same intervals. A metric with no value in any resample has no interval.
func bootstrap(scenarios []ScenarioResult, weights Weights, seed uint64, resamples int) map[string]Interval {
	if len(scenarios) == 0 || resamples <= 0 {
		return nil
	}

	rng := rand.New(rand.NewPCG(seed, seed)) //nolint:gosec // Resampling needs repeatable, not secret, numbers.
	sample := make([]ScenarioResult, len(scenarios))
	values := make(map[string][]float64, len(rateMetrics))

	for range resamples {
		for i := range sample {
			sample[i] = scenarios[rng.IntN(len(scenarios))]
		}

		m := metricsOf(sample, weights)

		for _, rm := range rateMetrics {
			if v := rm.value(m); v != nil {
				values[rm.key] = append(values[rm.key], *v)
			}
		}
	}

	tail := (1 - ConfidenceLevel) / 2
	intervals := make(map[string]Interval, len(values))

	for key, vs := range values {
		slices.Sort(vs)
		intervals[key] = Interval{Lower: quantile(vs, tail), Upper: quantile(vs, 1-tail)}
	}

	return intervals
}

// quantile returns the q quantile of sorted, which must not be empty, by
// linear interpolation between the two nearest values.
func quantile(sorted []float64, q float64) float64 {
	pos := q * float64(len(sorted)-1)
	lower := int(math.Floor(pos))
	upper := int(math.Ceil(pos))

	if lower == upper {
		return sorted[lower]
	}

	return sorted[lower] + (sorted[upper]-sorted[lower])*(pos-float64(lower))
}

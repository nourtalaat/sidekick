package bench

import (
	"encoding/json"
	"fmt"
)

// Difference says whether the confidence intervals of a metric in two runs
// overlap.
type Difference string

// The values of Difference.
const (
	// DifferenceClear: the intervals do not overlap, so the runs differ.
	DifferenceClear Difference = "clear"
	// DifferenceNone: the intervals overlap, so the runs may not differ.
	DifferenceNone Difference = "none"
	// DifferenceUnknown: one run has no interval for the metric.
	DifferenceUnknown Difference = "unknown"
)

// Keys of the metrics that CompareMetrics adds to the share metrics.
const (
	KeyMeanCostPerRunUSD = "meanCostPerRunUSD"
	KeyMeanDurationMs    = "meanDurationMs"
)

// MetricComparison is one metric of two runs, a and b.
type MetricComparison struct {
	// Key names the metric as report.json does.
	Key   string
	Label string
	// A and B are the values, nil when a run has nothing to count.
	A, B *float64
	// Change is B minus A, nil when either is nil.
	Change     *float64
	Difference Difference
}

// ParseStoredSummary decodes bench_runs.summary.
func ParseStoredSummary(raw []byte) (StoredSummary, error) {
	var s StoredSummary
	if err := json.Unmarshal(raw, &s); err != nil {
		return StoredSummary{}, fmt.Errorf("decode the summary of a benchmark run: %w", err)
	}

	return s, nil
}

// CompareMetrics lines up the metrics of two runs: each share metric with
// whether the intervals of the runs overlap, then the mean cost and the
// mean time of a run. It is what `sidekick bench compare` shows as rows, in
// a form a program can read.
func CompareMetrics(a, b StoredSummary) []MetricComparison {
	out := make([]MetricComparison, 0, len(rateMetrics)+2)

	for _, rm := range rateMetrics {
		va, vb := rm.value(a.Metrics), rm.value(b.Metrics)

		out = append(out, MetricComparison{
			Key: rm.key, Label: rm.label, A: va, B: vb, Change: changeOf(va, vb), Difference: differenceOf(a.Intervals, b.Intervals, rm.key),
		})
	}

	return append(out,
		MetricComparison{
			Key: KeyMeanCostPerRunUSD, Label: "mean cost per run", A: a.Metrics.MeanCostPerRunUSD, B: b.Metrics.MeanCostPerRunUSD,
			Change: changeOf(a.Metrics.MeanCostPerRunUSD, b.Metrics.MeanCostPerRunUSD), Difference: DifferenceUnknown,
		},
		MetricComparison{
			Key: KeyMeanDurationMs, Label: "mean time per run", A: a.Metrics.MeanDurationMs, B: b.Metrics.MeanDurationMs,
			Change: changeOf(a.Metrics.MeanDurationMs, b.Metrics.MeanDurationMs), Difference: DifferenceUnknown,
		},
	)
}

// changeOf is b minus a, or nil when either has no value.
func changeOf(a, b *float64) *float64 {
	if a == nil || b == nil {
		return nil
	}

	d := *b - *a

	return &d
}

// differenceOf says whether the intervals of the metric key in a and b
// overlap.
func differenceOf(a, b map[string]Interval, key string) Difference {
	ia, okA := a[key]
	ib, okB := b[key]

	switch {
	case !okA || !okB:
		return DifferenceUnknown
	case ia.overlaps(ib):
		return DifferenceNone
	default:
		return DifferenceClear
	}
}

package bench

import (
	"math"
	"slices"
	"time"

	"github.com/nourtalaat/sidekick/internal/report"
)

// Weights weigh the parts of the overall score. A part that has nothing to
// score, such as evidence when no scenario has evidence checks, is left
// out, and the other weights count for its share.
type Weights struct {
	Verdict   float64 `yaml:"verdict" json:"verdict"`
	RootCause float64 `yaml:"rootCause" json:"rootCause"`
	Evidence  float64 `yaml:"evidence" json:"evidence"`
}

// DefaultWeights are the weights of the overall score when bench.yaml sets
// none: half verdict accuracy, 30% root-cause match rate and 20% evidence
// score.
var DefaultWeights = Weights{Verdict: 0.5, RootCause: 0.3, Evidence: 0.2}

// Summary holds the run totals: the metrics over every run, and the
// interval of each share metric from resampling the scenarios.
type Summary struct {
	Metrics Metrics
	// Intervals are the ConfidenceLevel intervals of the share metrics, by
	// metric key, such as overall or fpPrecision. A metric with no value
	// has none.
	Intervals map[string]Interval
	Weights   Weights
	Seed      uint64
	Resamples int
}

// Metrics are the scores of a set of runs. A share is nil when it has
// nothing to count, such as false positive precision when no run gave a
// false positive verdict.
type Metrics struct {
	Runs int `json:"runs"`
	// VerdictAccuracy is the share of runs whose verdict counts as correct.
	VerdictAccuracy *float64 `json:"verdictAccuracy"`
	// FalsePositive and TruePositive are the precision and recall of each
	// class, by expected verdict.
	FalsePositive ClassMetrics `json:"falsePositive"`
	TruePositive  ClassMetrics `json:"truePositive"`
	// RootCauseMatch is the share of graded runs whose root cause matches.
	RootCauseMatch *float64 `json:"rootCauseMatch"`
	// RootCauseScore is the mean root-cause score of the graded runs.
	RootCauseScore *float64 `json:"rootCauseScore"`
	// Evidence is the mean evidence score of the runs with evidence checks.
	Evidence *float64 `json:"evidence"`
	// SourceFailuresReported is the share of runs with unavailable sources
	// to report that reported all of them.
	SourceFailuresReported *float64 `json:"sourceFailuresReported"`
	// Overall is the weighted overall score.
	Overall *float64 `json:"overall"`
	// JudgeErrors counts the runs the judge gave no grades for.
	JudgeErrors int `json:"judgeErrors"`

	ToolCalls    int   `json:"toolCalls"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
	// CostMicros is the cost of the runs' model calls, in micro-dollars;
	// JudgeCostMicros that of the judge's.
	CostMicros      int64 `json:"costMicros"`
	JudgeCostMicros int64 `json:"judgeCostMicros"`
	// MeanCostPerRunUSD is the mean cost of a run in US dollars.
	MeanCostPerRunUSD *float64 `json:"meanCostPerRunUSD"`
	// MeanDurationMs is the mean time of a run in milliseconds.
	MeanDurationMs *float64 `json:"meanDurationMs"`
}

// ClassMetrics are the precision and recall of one verdict class.
type ClassMetrics struct {
	// Precision is Hits over Predicted, Recall Hits over Expected.
	Precision *float64 `json:"precision"`
	Recall    *float64 `json:"recall"`
	// Predicted counts the runs that gave the verdict, Expected those whose
	// scenario expects it, and Hits those that did both.
	Predicted int `json:"predicted"`
	Expected  int `json:"expected"`
	Hits      int `json:"hits"`
}

// Spread is how the runs of one scenario vary.
type Spread struct {
	Runs int `json:"runs"`
	// Correct counts the runs whose verdict counts as correct.
	Correct int `json:"correct"`
	// RootCauseMean and RootCauseSD are the mean and the sample standard
	// deviation of the root-cause score of the graded runs.
	RootCauseMean *float64 `json:"rootCauseMean"`
	RootCauseSD   *float64 `json:"rootCauseSD"`
	CostUSD       Range    `json:"costUSD"`
	DurationMs    Range    `json:"durationMs"`
}

// Range is the smallest, middle and largest of a set of values.
type Range struct {
	Min    float64 `json:"min"`
	Median float64 `json:"median"`
	Max    float64 `json:"max"`
}

// rateMetric is a metric that is a share from 0 to 1, with the key that
// names it in report.json and the label tables show.
type rateMetric struct {
	key   string
	label string
	value func(Metrics) *float64
}

// rateMetrics are the share metrics, in the order tables show them. False
// positive precision comes before the other class metrics, because a wrong
// false positive hides a real incident.
var rateMetrics = []rateMetric{
	{key: "overall", label: "overall score", value: func(m Metrics) *float64 { return m.Overall }},
	{key: "verdictAccuracy", label: "verdict accuracy", value: func(m Metrics) *float64 { return m.VerdictAccuracy }},
	{key: "fpPrecision", label: "false positive precision", value: func(m Metrics) *float64 { return m.FalsePositive.Precision }},
	{key: "fpRecall", label: "false positive recall", value: func(m Metrics) *float64 { return m.FalsePositive.Recall }},
	{key: "tpPrecision", label: "true positive precision", value: func(m Metrics) *float64 { return m.TruePositive.Precision }},
	{key: "tpRecall", label: "true positive recall", value: func(m Metrics) *float64 { return m.TruePositive.Recall }},
	{key: "rootCauseMatch", label: "root cause match", value: func(m Metrics) *float64 { return m.RootCauseMatch }},
	{key: "rootCauseScore", label: "root cause score", value: func(m Metrics) *float64 { return m.RootCauseScore }},
	{key: "evidence", label: "evidence", value: func(m Metrics) *float64 { return m.Evidence }},
	{key: "sourceFailuresReported", label: "source failures reported", value: func(m Metrics) *float64 { return m.SourceFailuresReported }},
}

// summarize returns the run totals of scenarios.
func summarize(scenarios []ScenarioResult, opts ScoreOptions) Summary {
	weights := opts.Weights
	if weights == (Weights{}) {
		weights = DefaultWeights
	}

	resamples := opts.Resamples
	if resamples == 0 {
		resamples = BootstrapResamples
	}

	return Summary{
		Metrics:   metricsOf(scenarios, weights),
		Intervals: bootstrap(scenarios, weights, opts.Seed, resamples),
		Weights:   weights,
		Seed:      opts.Seed,
		Resamples: resamples,
	}
}

// metricsOf returns the metrics over every run of scenarios.
func metricsOf(scenarios []ScenarioResult, weights Weights) Metrics {
	var (
		m                                          Metrics
		correct, graded, matched, evidenceRuns     int
		sourceRuns, sourcesReported                int
		rootCauseTotal, evidenceTotal, durationSum float64
	)

	for _, sc := range scenarios {
		for _, run := range sc.Runs {
			m.Runs++
			m.ToolCalls += run.ToolCalls
			m.InputTokens += run.InputTokens
			m.OutputTokens += run.OutputTokens
			m.CostMicros += run.CostMicros
			durationSum += float64(run.Duration) / float64(time.Millisecond)

			if run.VerdictCorrect {
				correct++
			}

			countClass(&m.FalsePositive, report.FalsePositive, sc.Expected.Verdict, run.Report.Verdict)
			countClass(&m.TruePositive, report.TruePositive, sc.Expected.Verdict, run.Report.Verdict)

			if rc := run.RootCause; rc != nil {
				graded++
				rootCauseTotal += rc.Score
				m.JudgeCostMicros += rc.CostMicros

				if rc.Match {
					matched++
				}

				if rc.Error != "" {
					m.JudgeErrors++
				}
			}

			if run.EvidenceScore != nil {
				evidenceRuns++
				evidenceTotal += *run.EvidenceScore
			}

			if run.SourcesReported != nil {
				sourceRuns++

				if *run.SourcesReported {
					sourcesReported++
				}
			}
		}
	}

	m.VerdictAccuracy = ratio(correct, m.Runs)
	m.FalsePositive.finish()
	m.TruePositive.finish()
	m.RootCauseMatch = ratio(matched, graded)
	m.RootCauseScore = mean(rootCauseTotal, graded)
	m.Evidence = mean(evidenceTotal, evidenceRuns)
	m.SourceFailuresReported = ratio(sourcesReported, sourceRuns)
	m.Overall = overall(weights, m)
	m.MeanCostPerRunUSD = mean(float64(m.CostMicros)/microsPerUSD, m.Runs)
	m.MeanDurationMs = mean(durationSum, m.Runs)

	return m
}

// finish sets the precision and recall from the counts.
func (c *ClassMetrics) finish() {
	c.Precision = ratio(c.Hits, c.Predicted)
	c.Recall = ratio(c.Hits, c.Expected)
}

// countClass counts one run in the metrics of class.
func countClass(c *ClassMetrics, class, expected, got report.Verdict) {
	if got == class {
		c.Predicted++
	}

	if expected == class {
		c.Expected++
	}

	if got == class && expected == class {
		c.Hits++
	}
}

// overall returns the weighted overall score of m, leaving out the parts
// that have no value, or nil when none has one.
func overall(w Weights, m Metrics) *float64 {
	var sum, weight float64

	for _, part := range []struct {
		weight float64
		value  *float64
	}{
		{w.Verdict, m.VerdictAccuracy},
		{w.RootCause, m.RootCauseMatch},
		{w.Evidence, m.Evidence},
	} {
		if part.value == nil || part.weight == 0 {
			continue
		}

		sum += part.weight * *part.value
		weight += part.weight
	}

	if weight == 0 {
		return nil
	}

	score := sum / weight

	return &score
}

// spreadOf returns the spread of the runs of one scenario.
func spreadOf(runs []RunResult) Spread {
	s := Spread{Runs: len(runs)}

	var scores, costs, durations []float64

	for _, run := range runs {
		if run.VerdictCorrect {
			s.Correct++
		}

		if run.RootCause != nil {
			scores = append(scores, run.RootCause.Score)
		}

		costs = append(costs, run.CostUSD())
		durations = append(durations, float64(run.Duration)/float64(time.Millisecond))
	}

	if len(scores) > 0 {
		m, sd := meanSD(scores)
		s.RootCauseMean, s.RootCauseSD = &m, &sd
	}

	s.CostUSD = rangeOf(costs)
	s.DurationMs = rangeOf(durations)

	return s
}

// meanSD returns the mean and the sample standard deviation of values,
// which must not be empty. One value has a deviation of 0.
func meanSD(values []float64) (avg, sd float64) {
	var sum float64
	for _, v := range values {
		sum += v
	}

	avg = sum / float64(len(values))

	if len(values) < 2 {
		return avg, 0
	}

	var squares float64
	for _, v := range values {
		squares += (v - avg) * (v - avg)
	}

	return avg, math.Sqrt(squares / float64(len(values)-1))
}

// rangeOf returns the smallest, median and largest of values, or the zero
// Range when there are none. The median of an even number of values is the
// mean of the middle two.
func rangeOf(values []float64) Range {
	if len(values) == 0 {
		return Range{}
	}

	sorted := slices.Sorted(slices.Values(values))
	n := len(sorted)

	median := sorted[n/2]
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}

	return Range{Min: sorted[0], Median: median, Max: sorted[n-1]}
}

// mean returns sum over n, or nil when n is zero.
func mean(sum float64, n int) *float64 {
	if n == 0 {
		return nil
	}

	m := sum / float64(n)

	return &m
}

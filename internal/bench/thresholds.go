package bench

import (
	"fmt"
	"io"
	"text/tabwriter"
)

// DefaultMaxDrop is how far the overall score may fall below the
// baseline's when bench.yaml sets a baseline but no maxDrop.
const DefaultMaxDrop = 0.02

// Threshold names, as report.json and the tables show them.
const (
	thresholdVerdictAccuracy = "verdictAccuracy"
	thresholdFPPrecision     = "fpPrecision"
	thresholdRootCauseMatch  = "rootCauseMatch"
	thresholdMaxCostPerRun   = "maxCostPerRunUSD"
	thresholdBaseline        = "baseline"
)

// nothingToCount is the detail of a threshold whose metric has no value.
const nothingToCount = "nothing to count, so it passes"

// Thresholds are the scores a run must reach, in bench.yaml. A zero
// threshold is not checked.
type Thresholds struct {
	// VerdictAccuracy, FPPrecision and RootCauseMatch are the lowest
	// shares allowed.
	VerdictAccuracy float64 `yaml:"verdictAccuracy"`
	FPPrecision     float64 `yaml:"fpPrecision"`
	RootCauseMatch  float64 `yaml:"rootCauseMatch"`
	// MaxCostPerRunUSD is the highest mean cost of a run allowed.
	MaxCostPerRunUSD float64 `yaml:"maxCostPerRunUSD"`
	// Baseline is a report.json the overall score is compared with.
	Baseline string `yaml:"baseline"`
	// MaxDrop is how far the overall score may fall below the baseline's.
	// Nil means DefaultMaxDrop.
	MaxDrop *float64 `yaml:"maxDrop"`
}

// ThresholdResult is the check of one threshold.
type ThresholdResult struct {
	Name string `json:"name"`
	// Limit is the threshold; Value the run's metric, nil when it has
	// nothing to count.
	Limit  float64  `json:"limit"`
	Value  *float64 `json:"value"`
	Passed bool     `json:"passed"`
	Detail string   `json:"detail,omitempty"`
}

// Check checks summary against each threshold that is set, and the
// overall score against baseline's when baseline is not nil. A metric
// with nothing to count passes.
func (t Thresholds) Check(summary Summary, baseline *JSONReport) []ThresholdResult {
	m := summary.Metrics

	var out []ThresholdResult

	for _, share := range []struct {
		name  string
		limit float64
		value *float64
	}{
		{thresholdVerdictAccuracy, t.VerdictAccuracy, m.VerdictAccuracy},
		{thresholdFPPrecision, t.FPPrecision, m.FalsePositive.Precision},
		{thresholdRootCauseMatch, t.RootCauseMatch, m.RootCauseMatch},
	} {
		if share.limit > 0 {
			out = append(out, atLeast(share.name, share.limit, share.value))
		}
	}

	if t.MaxCostPerRunUSD > 0 {
		r := ThresholdResult{Name: thresholdMaxCostPerRun, Limit: t.MaxCostPerRunUSD, Value: m.MeanCostPerRunUSD, Passed: true}

		switch {
		case r.Value == nil:
			r.Detail = nothingToCount
		case *r.Value > r.Limit:
			r.Passed = false
		}

		out = append(out, r)
	}

	if baseline != nil {
		out = append(out, t.checkBaseline(summary, baseline))
	}

	return out
}

// checkBaseline checks that the overall score is no more than MaxDrop
// below the baseline's, and says whether the two intervals show a clear
// difference.
func (t Thresholds) checkBaseline(summary Summary, baseline *JSONReport) ThresholdResult {
	maxDrop := DefaultMaxDrop
	if t.MaxDrop != nil {
		maxDrop = *t.MaxDrop
	}

	if baseline.Metrics.Overall == nil {
		return ThresholdResult{Name: thresholdBaseline, Value: summary.Metrics.Overall, Passed: true, Detail: "the baseline has no overall score, so it passes"}
	}

	r := atLeast(thresholdBaseline, *baseline.Metrics.Overall-maxDrop, summary.Metrics.Overall)
	if r.Value == nil {
		return r
	}

	r.Detail = fmt.Sprintf("baseline %.2f, at most %.2f lower", *baseline.Metrics.Overall, maxDrop)

	if flag := differenceFlag(summary.Intervals, baseline.Intervals, "overall"); flag != noValue {
		r.Detail += "; " + flag
	}

	return r
}

// validate returns the problems of the thresholds as "<field>: <problem>".
func (t Thresholds) validate() []string {
	var problems []string

	for _, share := range []struct {
		key   string
		value float64
	}{
		{"thresholds.verdictAccuracy", t.VerdictAccuracy},
		{"thresholds.fpPrecision", t.FPPrecision},
		{"thresholds.rootCauseMatch", t.RootCauseMatch},
	} {
		if share.value < 0 || share.value > 1 {
			problems = append(problems, fmt.Sprintf("%s: must be from 0 to 1, not %g", share.key, share.value))
		}
	}

	if t.MaxCostPerRunUSD < 0 {
		problems = append(problems, "thresholds.maxCostPerRunUSD: must not be negative")
	}

	if t.MaxDrop != nil {
		switch {
		case t.Baseline == "":
			problems = append(problems, "thresholds.maxDrop: set thresholds.baseline too; maxDrop is measured from it")
		case *t.MaxDrop < 0 || *t.MaxDrop > 1:
			problems = append(problems, fmt.Sprintf("thresholds.maxDrop: must be from 0 to 1, not %g", *t.MaxDrop))
		}
	}

	return problems
}

// WriteThresholds writes one line per threshold: its name, limit, the
// run's value and whether it passed.
func WriteThresholds(w io.Writer, results []ThresholdResult) error {
	if len(results) == 0 {
		return nil
	}

	tw := tabwriter.NewWriter(w, tableMinWidth, tableTabWidth, tablePadding, tablePadChar, 0)

	if _, err := fmt.Fprintln(tw, "\nTHRESHOLD\tLIMIT\tVALUE\tRESULT"); err != nil {
		return fmt.Errorf("write the thresholds header: %w", err)
	}

	for _, r := range results {
		result := "passed"
		if !r.Passed {
			result = "FAILED"
		}

		if r.Detail != "" {
			result += " (" + r.Detail + ")"
		}

		if _, err := fmt.Fprintf(tw, "%s\t%.4g\t%s\t%s\n", r.Name, r.Limit, valueCell(r.Value), result); err != nil {
			return fmt.Errorf("write the %s threshold: %w", r.Name, err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write the thresholds: %w", err)
	}

	return nil
}

// Failed returns the thresholds that did not pass.
func Failed(results []ThresholdResult) []ThresholdResult {
	var out []ThresholdResult

	for _, r := range results {
		if !r.Passed {
			out = append(out, r)
		}
	}

	return out
}

// atLeast checks that value is at least limit. A nil value passes.
func atLeast(name string, limit float64, value *float64) ThresholdResult {
	r := ThresholdResult{Name: name, Limit: limit, Value: value, Passed: true}

	switch {
	case value == nil:
		r.Detail = nothingToCount
	case *value < limit:
		r.Passed = false
	}

	return r
}

// valueCell formats a value with up to four significant digits, or -
// when it is nil.
func valueCell(v *float64) string {
	if v == nil {
		return noValue
	}

	return fmt.Sprintf("%.4g", *v)
}

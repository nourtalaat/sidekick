//go:generate mockgen -source score.go -destination mocks_score.go -package bench -write_package_comment=false

package bench

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/report"
)

// RootCauseMatchScore is the lowest root-cause score of a run whose root
// cause matches. Every required rubric item must be met as well.
const RootCauseMatchScore = 0.7

// noReportReason is the reason of each rubric item of a run that ended
// without a report, which is not sent to the judge.
const noReportReason = "The run ended without a report."

// Grader grades the root cause of a report against the expected one, item
// by item of its rubric. The judge is the Grader of `sidekick bench run`.
type Grader interface {
	Grade(ctx context.Context, expected RootCause, rep report.Report) (Grade, error)
}

// Grade is the grading of one report: one ItemGrade per rubric item, and
// what the grading cost.
type Grade struct {
	Items        []ItemGrade
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
}

// ItemGrade says whether a report meets one rubric item, and why.
type ItemGrade struct {
	ID     string `json:"id"`
	Met    bool   `json:"met"`
	Reason string `json:"reason"`
}

// RootCauseResult is the root-cause score of one run.
type RootCauseResult struct {
	// Items are the grades of the rubric items, in rubric order.
	Items []ItemGrade
	// Score is the weight of the items met over the weight of all items.
	Score float64
	// RequiredMet is true when every required item is met.
	RequiredMet bool
	// Match is true when Score is at least RootCauseMatchScore and every
	// required item is met.
	Match bool
	// Error says why the run has no grades, for example a judge that gave
	// none after its attempts. Such a run scores 0 and does not match.
	Error        string
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
}

// EvidenceResult is the outcome of one evidence check of a run.
type EvidenceResult struct {
	Check  EvidenceCheck
	Passed bool
}

// ScoreOptions change how Score scores.
type ScoreOptions struct {
	// Grader grades root causes. Nil leaves them ungraded.
	Grader Grader
	// Weights weigh the parts of the overall score. The zero value means
	// DefaultWeights.
	Weights Weights
	// Seed seeds the resampling of the run totals' intervals, so the same
	// results always give the same intervals.
	Seed uint64
	// Resamples is how many resamples the intervals are computed from.
	// Zero means BootstrapResamples.
	Resamples int
}

// Score scores each run of results, then the spread of each scenario and
// the run totals. Root causes are graded by opts.Grader, one run at a
// time. A grading that fails because of the model provider, or because
// ctx ended, stops Score with its error; any other grading failure is kept
// on the run, which then scores 0 for its root cause.
func Score(ctx context.Context, results *Results, opts ScoreOptions) error {
	for i := range results.Scenarios {
		sc := &results.Scenarios[i]

		for j := range sc.Runs {
			if err := scoreRun(ctx, sc.Expected, &sc.Runs[j], opts.Grader); err != nil {
				return fmt.Errorf("score scenario %s, run %d: %w", sc.ID, sc.Runs[j].Run, err)
			}
		}

		spread := spreadOf(sc.Runs)
		sc.Spread = &spread
	}

	summary := summarize(results.Scenarios, opts)
	results.Summary = &summary

	return nil
}

// scoreRun sets the scores of one run.
func scoreRun(ctx context.Context, expected Expected, run *RunResult, grader Grader) error {
	run.Scored = true
	run.VerdictCorrect = expected.accepts(run.Report.Verdict)
	run.Evidence, run.EvidenceScore = checkEvidence(expected.Evidence, *run)
	run.SourcesReported, run.MissingSources = reportedSources(expected.UnavailableReported, run.Report)

	rubric := expected.RootCause.Rubric
	if grader == nil || len(rubric) == 0 {
		return nil
	}

	if run.Report.Verdict == "" {
		rc := rootCauseResult(rubric, Grade{Items: unmet(rubric, noReportReason)})
		run.RootCause = &rc

		return nil
	}

	grade, err := grader.Grade(ctx, expected.RootCause, run.Report)
	if err != nil {
		if errors.Is(err, ErrInfrastructure) || ctx.Err() != nil {
			return fmt.Errorf("grade the root cause: %w", err)
		}

		run.RootCause = &RootCauseResult{Error: err.Error()}

		return nil
	}

	rc := rootCauseResult(rubric, grade)
	run.RootCause = &rc

	return nil
}

// accepts reports whether verdict counts as correct: it is the expected
// verdict or one of the acceptable ones.
func (e Expected) accepts(verdict report.Verdict) bool {
	return verdict == e.Verdict || slices.Contains(e.Acceptable, verdict)
}

// checkEvidence runs the evidence checks on a run, and returns each
// outcome and the share passed, or nil for both when there are no checks.
// A calledTool check passes when the run called the tool, whatever the
// call returned; a citedURL check when a finding of the report cites the
// URL.
func checkEvidence(checks []EvidenceCheck, run RunResult) ([]EvidenceResult, *float64) {
	if len(checks) == 0 {
		return nil, nil
	}

	results := make([]EvidenceResult, len(checks))
	passed := 0

	for i, check := range checks {
		ok := false

		switch {
		case check.CalledTool != "":
			ok = slices.ContainsFunc(run.ToolResults, func(r ToolResult) bool { return r.Tool == check.CalledTool })
		case check.CitedURL != "":
			ok = cites(run.Report, check.CitedURL)
		}

		results[i] = EvidenceResult{Check: check, Passed: ok}

		if ok {
			passed++
		}
	}

	return results, ratio(passed, len(checks))
}

// cites reports whether a finding of rep cites url.
func cites(rep report.Report, url string) bool {
	for _, f := range rep.Findings {
		for _, e := range f.Evidence {
			if e.URL == url {
				return true
			}
		}
	}

	return false
}

// reportedSources checks that each source in names is unavailable in the
// report's checked list. It returns whether all of them are, and the ones
// that are not, or nil for both when names is empty.
func reportedSources(names []string, rep report.Report) (*bool, []string) {
	if len(names) == 0 {
		return nil, nil
	}

	var missing []string

	for _, name := range names {
		reported := slices.ContainsFunc(rep.Checked, func(c report.Check) bool {
			return c.Source == name && c.Status == report.CheckUnavailable
		})
		if !reported {
			missing = append(missing, name)
		}
	}

	all := len(missing) == 0

	return &all, missing
}

// rootCauseResult scores a grade against its rubric. An item the grade
// leaves out counts as not met.
func rootCauseResult(rubric []RubricItem, grade Grade) RootCauseResult {
	byID := make(map[string]ItemGrade, len(grade.Items))
	for _, g := range grade.Items {
		byID[g.ID] = g
	}

	out := RootCauseResult{
		RequiredMet:  true,
		InputTokens:  grade.InputTokens,
		OutputTokens: grade.OutputTokens,
		CostMicros:   grade.CostMicros,
	}

	var total, met float64

	for _, item := range rubric {
		g, ok := byID[item.ID]
		if !ok {
			g = ItemGrade{ID: item.ID, Reason: "The judge did not grade this item."}
		}

		out.Items = append(out.Items, g)
		total += item.Weight

		switch {
		case g.Met:
			met += item.Weight
		case item.Required:
			out.RequiredMet = false
		}
	}

	if total > 0 {
		out.Score = met / total
	}

	out.Match = out.RequiredMet && out.Score >= RootCauseMatchScore

	return out
}

// unmet returns a grade of not met, with reason, for each rubric item.
func unmet(rubric []RubricItem, reason string) []ItemGrade {
	items := make([]ItemGrade, len(rubric))
	for i, item := range rubric {
		items[i] = ItemGrade{ID: item.ID, Reason: reason}
	}

	return items
}

// ratio returns n over d, or nil when d is zero.
func ratio(n, d int) *float64 {
	if d == 0 {
		return nil
	}

	r := float64(n) / float64(d)

	return &r
}

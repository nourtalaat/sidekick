package bench

import (
	"fmt"
	"io"
	"text/tabwriter"
	"time"

	"github.com/google/uuid"

	"github.com/nourtalaat/sidekick/internal/report"
)

// microsPerUSD converts the micro-dollars runs are priced in to dollars.
const microsPerUSD = 1_000_000

// percent turns a share into a percentage.
const percent = 100

// noValue is the table cell of a score a run or metric does not have.
const noValue = "-"

// Table layout of WriteTable.
const (
	tableMinWidth = 0
	tableTabWidth = 8
	tablePadding  = 2
	tablePadChar  = ' '
)

// Results are the results of one `sidekick bench run`: one ScenarioResult
// per scenario, with one RunResult per run of it. Score adds the scores of
// each run, the spread of each scenario and the run totals.
type Results struct {
	StartedAt  time.Time
	FinishedAt time.Time
	// Repeat is how many times each scenario ran.
	Repeat    int
	Scenarios []ScenarioResult
	// Summary holds the run totals, once Score has run.
	Summary *Summary
}

// ScenarioResult holds the runs of one scenario.
type ScenarioResult struct {
	ID    string
	Title string
	// Path is the scenario file.
	Path string
	// Expected is the outcome the runs are scored against.
	Expected Expected
	Runs     []RunResult
	// Spread is how the runs vary, once Score has run.
	Spread *Spread
}

// RunResult is the outcome of one run of a scenario.
type RunResult struct {
	// Run counts the scenario's runs from 1.
	Run             int
	InvestigationID uuid.UUID
	// Status is the investigation's status at the end: final, or failed
	// when the model refused the request.
	Status string
	// Report is the investigation's report, with its verdict, findings,
	// checked sources and stop reason.
	Report       report.Report
	ToolCalls    int
	InputTokens  int64
	OutputTokens int64
	CostMicros   int64
	// Duration is the time from the delivery of the payload to the end of
	// the investigation.
	Duration time.Duration
	// ToolResults are the results of the run's source calls, in order.
	// Built-in tools such as submit_verdict are left out.
	ToolResults []ToolResult

	// Scored is true once Score has set the fields below.
	Scored bool
	// VerdictCorrect is true when the verdict is the expected one or an
	// acceptable one.
	VerdictCorrect bool
	// RootCause is the graded root cause, or nil when the scenario has no
	// rubric or no grader ran.
	RootCause *RootCauseResult
	// Evidence are the outcomes of the evidence checks, and EvidenceScore
	// the share passed, nil when the scenario has no checks.
	Evidence      []EvidenceResult
	EvidenceScore *float64
	// SourcesReported is true when every source the scenario expects to be
	// reported as unavailable is, and nil when it expects none.
	// MissingSources are the ones that are not.
	SourcesReported *bool
	MissingSources  []string
}

// ToolResult is the result of one source call, as the model read it.
type ToolResult struct {
	// Tool is the tool as the model names it, for example
	// github__list_commits.
	Tool string
	// Status is how the call ended, for example ok, no_data or
	// unavailable.
	Status  string
	Content string
}

// WriteTable writes one line per run: the scenario, the run, the verdict
// and the expected one, whether the verdict counts as correct, the
// root-cause and evidence scores, how the run stopped, its tool calls,
// tokens, cost and time. A score the run does not have shows as "-".
func (r *Results) WriteTable(w io.Writer) error {
	tw := tabwriter.NewWriter(w, tableMinWidth, tableTabWidth, tablePadding, tablePadChar, 0)

	if _, err := fmt.Fprintln(tw, "SCENARIO\tRUN\tSTATUS\tVERDICT\tEXPECTED\tCORRECT\tROOT CAUSE\tEVIDENCE\tSTOP\tTOOL CALLS\tTOKENS\tCOST\tTIME"); err != nil {
		return fmt.Errorf("write the table header: %w", err)
	}

	for _, s := range r.Scenarios {
		for _, run := range s.Runs {
			_, err := fmt.Fprintf(tw, "%s\t%d/%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%d\t%d\t$%.4f\t%s\n",
				s.ID, run.Run, r.Repeat, run.Status, run.Report.Verdict, s.Expected.Verdict,
				run.correctCell(), run.rootCauseCell(), shareCell(run.EvidenceScore), run.Report.StopReason,
				run.ToolCalls, run.InputTokens+run.OutputTokens, run.CostUSD(), run.Duration.Round(time.Millisecond))
			if err != nil {
				return fmt.Errorf("write the row of %s run %d: %w", s.ID, run.Run, err)
			}
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write the table: %w", err)
	}

	return nil
}

// WriteSummary writes, once Score has run, the spread of each scenario and
// the run totals with their intervals. It writes nothing before that.
func (r *Results) WriteSummary(w io.Writer) error {
	if r.Summary == nil {
		return nil
	}

	tw := tabwriter.NewWriter(w, tableMinWidth, tableTabWidth, tablePadding, tablePadChar, 0)

	if _, err := fmt.Fprintln(tw, "\nSCENARIO\tCORRECT\tROOT CAUSE\tCOST MIN / MEDIAN / MAX\tTIME MIN / MEDIAN / MAX"); err != nil {
		return fmt.Errorf("write the spread header: %w", err)
	}

	for _, s := range r.Scenarios {
		if s.Spread == nil {
			continue
		}

		sp := s.Spread

		_, err := fmt.Fprintf(tw, "%s\t%d/%d\t%s\t$%.4f / $%.4f / $%.4f\t%s / %s / %s\n",
			s.ID, sp.Correct, sp.Runs, meanSDCell(sp.RootCauseMean, sp.RootCauseSD),
			sp.CostUSD.Min, sp.CostUSD.Median, sp.CostUSD.Max,
			msCell(sp.DurationMs.Min), msCell(sp.DurationMs.Median), msCell(sp.DurationMs.Max))
		if err != nil {
			return fmt.Errorf("write the spread of %s: %w", s.ID, err)
		}
	}

	if _, err := fmt.Fprintf(tw, "\nMETRIC\tVALUE\t%.0f%% INTERVAL\n", ConfidenceLevel*percent); err != nil {
		return fmt.Errorf("write the totals header: %w", err)
	}

	m := r.Summary.Metrics

	for _, rm := range rateMetrics {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\n", rm.label, shareCell(rm.value(m)), intervalCell(r.Summary.Intervals, rm.key)); err != nil {
			return fmt.Errorf("write the %s row: %w", rm.label, err)
		}
	}

	_, err := fmt.Fprintf(tw, "runs\t%d\ncost\t$%.4f\njudge cost\t$%.4f\njudge errors\t%d\n",
		m.Runs, float64(m.CostMicros)/microsPerUSD, float64(m.JudgeCostMicros)/microsPerUSD, m.JudgeErrors)
	if err != nil {
		return fmt.Errorf("write the totals: %w", err)
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write the summary: %w", err)
	}

	return nil
}

// CostUSD returns the cost of the run's model calls in US dollars.
func (r RunResult) CostUSD() float64 {
	return float64(r.CostMicros) / microsPerUSD
}

// correctCell is the CORRECT cell of the run: yes, no, or - before Score.
func (r RunResult) correctCell() string {
	switch {
	case !r.Scored:
		return noValue
	case r.VerdictCorrect:
		return "yes"
	default:
		return "no"
	}
}

// rootCauseCell is the ROOT CAUSE cell of the run: its score, "error" when
// the judge gave no grades, or - when it was not graded.
func (r RunResult) rootCauseCell() string {
	switch {
	case r.RootCause == nil:
		return noValue
	case r.RootCause.Error != "":
		return "error"
	default:
		return fmt.Sprintf("%.2f", r.RootCause.Score)
	}
}

// shareCell formats a share with two decimals, or - when it is nil.
func shareCell(v *float64) string {
	if v == nil {
		return noValue
	}

	return fmt.Sprintf("%.2f", *v)
}

// meanSDCell formats a mean and a standard deviation as "0.83 ± 0.10", or
// - when there is no mean.
func meanSDCell(avg, sd *float64) string {
	if avg == nil || sd == nil {
		return noValue
	}

	return fmt.Sprintf("%.2f ± %.2f", *avg, *sd)
}

// intervalCell formats the interval of the metric key as "0.85 to 0.97",
// or - when it has none.
func intervalCell(intervals map[string]Interval, key string) string {
	i, ok := intervals[key]
	if !ok {
		return noValue
	}

	return fmt.Sprintf("%.2f to %.2f", i.Lower, i.Upper)
}

// msCell formats milliseconds as a duration rounded to the millisecond.
func msCell(ms float64) string {
	return time.Duration(ms * float64(time.Millisecond)).Round(time.Millisecond).String()
}

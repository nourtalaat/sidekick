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

// Table layout of WriteTable.
const (
	tableMinWidth = 0
	tableTabWidth = 8
	tablePadding  = 2
	tablePadChar  = ' '
)

// Results are the results of one `sidekick bench run`: one ScenarioResult
// per scenario, with one RunResult per run of it. Scoring adds its scores
// to ScenarioResult and RunResult.
type Results struct {
	StartedAt  time.Time
	FinishedAt time.Time
	// Repeat is how many times each scenario ran.
	Repeat    int
	Scenarios []ScenarioResult
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
// and the expected one, how the run stopped, its tool calls, tokens, cost
// and time.
func (r *Results) WriteTable(w io.Writer) error {
	tw := tabwriter.NewWriter(w, tableMinWidth, tableTabWidth, tablePadding, tablePadChar, 0)

	if _, err := fmt.Fprintln(tw, "SCENARIO\tRUN\tSTATUS\tVERDICT\tEXPECTED\tSTOP\tTOOL CALLS\tTOKENS\tCOST\tTIME"); err != nil {
		return fmt.Errorf("write the table header: %w", err)
	}

	for _, s := range r.Scenarios {
		for _, run := range s.Runs {
			_, err := fmt.Fprintf(tw, "%s\t%d/%d\t%s\t%s\t%s\t%s\t%d\t%d\t$%.4f\t%s\n",
				s.ID, run.Run, r.Repeat, run.Status, run.Report.Verdict, s.Expected.Verdict, run.Report.StopReason,
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

// CostUSD returns the cost of the run's model calls in US dollars.
func (r RunResult) CostUSD() float64 {
	return float64(r.CostMicros) / microsPerUSD
}

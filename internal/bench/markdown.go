package bench

import (
	"fmt"
	"io"
	"strings"

	"github.com/nourtalaat/sidekick/internal/model"
)

// WriteMarkdown writes a summary of the scored results as Markdown, for a
// pull request comment: what ran, the run totals with their intervals, and
// one row per scenario.
func (r *Results) WriteMarkdown(w io.Writer, info RunInfo) error {
	if r.Summary == nil {
		return errNotScored
	}

	var b strings.Builder

	b.WriteString("## Sidekick benchmark\n\n")
	fmt.Fprintf(&b, "Sidekick %s (%s), %d scenarios, %d runs each", info.SidekickVersion, info.GitSHA, len(r.Scenarios), r.Repeat)

	if c, ok := info.Connectors[model.RoleInvestigate]; ok {
		fmt.Fprintf(&b, ", investigated by %s (%s)", c.Connector, c.Model)
	}

	if c, ok := info.Connectors[model.RoleJudge]; ok {
		fmt.Fprintf(&b, ", judged by %s (%s)", c.Connector, c.Model)
	}

	fmt.Fprintf(&b, ". Prompt %s, judge prompt %s.\n\n", info.PromptVersion, info.JudgePromptVersion)

	m := r.Summary.Metrics

	fmt.Fprintf(&b, "| Metric | Value | %.0f%% interval |\n|---|---|---|\n", ConfidenceLevel*percent)

	for _, rm := range rateMetrics {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", rm.label, shareCell(rm.value(m)), intervalCell(r.Summary.Intervals, rm.key))
	}

	fmt.Fprintf(&b, "| runs | %d | |\n| cost | $%.4f | |\n| judge errors | %d | |\n\n",
		m.Runs, float64(m.CostMicros)/microsPerUSD, m.JudgeErrors)

	b.WriteString("| Scenario | Correct | Root cause | Median cost | Median time |\n|---|---|---|---|---|\n")

	for _, sc := range r.Scenarios {
		if sc.Spread == nil {
			continue
		}

		sp := sc.Spread
		fmt.Fprintf(&b, "| %s | %d/%d | %s | $%.4f | %s |\n",
			sc.ID, sp.Correct, sp.Runs, meanSDCell(sp.RootCauseMean, sp.RootCauseSD), sp.CostUSD.Median, msCell(sp.DurationMs.Median))
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write the Markdown summary: %w", err)
	}

	return nil
}

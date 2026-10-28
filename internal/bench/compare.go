package bench

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/nourtalaat/sidekick/internal/model"
)

// Flags of the DIFFERENCE column of a comparison.
const (
	noClearDifference = "no clear difference"
	clearDifference   = "clear difference"
)

// WriteComparison writes two benchmark reports side by side: what each run
// used, then each metric with the change from a to b and, for each share,
// whether their intervals overlap ("no clear difference") or not, then
// each scenario's runs.
func WriteComparison(w io.Writer, a, b *JSONReport) error {
	tw := tabwriter.NewWriter(w, tableMinWidth, tableTabWidth, tablePadding, tablePadChar, 0)

	var rows []string

	rows = append(rows, infoRows(a, b)...)
	rows = append(rows, "", "METRIC\tA\tB\tCHANGE\tDIFFERENCE")
	rows = append(rows, metricRows(a, b)...)
	rows = append(rows, "", "SCENARIO\tA CORRECT\tB CORRECT\tA ROOT CAUSE\tB ROOT CAUSE")
	rows = append(rows, scenarioRows(a, b)...)

	for _, row := range rows {
		if _, err := fmt.Fprintln(tw, row); err != nil {
			return fmt.Errorf("write the comparison: %w", err)
		}
	}

	if err := tw.Flush(); err != nil {
		return fmt.Errorf("write the comparison: %w", err)
	}

	return nil
}

// infoRows are the rows of what each run used.
func infoRows(a, b *JSONReport) []string {
	rows := []string{
		"\tA\tB",
		row("sidekick", build(a), build(b)),
		row("scenarios", fmt.Sprintf("%s (%d)", a.ScenarioSet, len(a.Scenarios)), fmt.Sprintf("%s (%d)", b.ScenarioSet, len(b.Scenarios))),
		row("repeat", fmt.Sprint(a.Repeat), fmt.Sprint(b.Repeat)),
	}

	roles := slices.Sorted(maps.Keys(a.Connectors))
	for _, role := range slices.Sorted(maps.Keys(b.Connectors)) {
		if !slices.Contains(roles, role) {
			roles = append(roles, role)
		}
	}

	for _, role := range roles {
		rows = append(rows, row(string(role), connectorCell(a.Connectors, role), connectorCell(b.Connectors, role)))
	}

	return append(rows,
		row("prompt", a.PromptVersion, b.PromptVersion),
		row("judge prompt", a.JudgePromptVersion, b.JudgePromptVersion),
		row("strategies", strategiesCell(a.Strategies), strategiesCell(b.Strategies)),
	)
}

// metricRows are the rows of the metrics.
func metricRows(a, b *JSONReport) []string {
	var rows []string

	for _, rm := range rateMetrics {
		va, vb := rm.value(a.Metrics), rm.value(b.Metrics)

		change := noValue
		if va != nil && vb != nil {
			change = fmt.Sprintf("%+.2f", *vb-*va)
		}

		rows = append(rows, row(rm.label, shareCell(va), shareCell(vb), change, differenceFlag(a.Intervals, b.Intervals, rm.key)))
	}

	ma, mb := a.Metrics, b.Metrics

	return append(rows,
		row("mean cost per run", costCell(ma.MeanCostPerRunUSD), costCell(mb.MeanCostPerRunUSD), costChange(ma.MeanCostPerRunUSD, mb.MeanCostPerRunUSD), noValue),
		row("mean time per run", durationCell(ma.MeanDurationMs), durationCell(mb.MeanDurationMs), durationChange(ma.MeanDurationMs, mb.MeanDurationMs), noValue),
		row("runs", fmt.Sprint(ma.Runs), fmt.Sprint(mb.Runs), fmt.Sprintf("%+d", mb.Runs-ma.Runs), noValue),
		row("judge errors", fmt.Sprint(ma.JudgeErrors), fmt.Sprint(mb.JudgeErrors), fmt.Sprintf("%+d", mb.JudgeErrors-ma.JudgeErrors), noValue),
	)
}

// scenarioRows are the rows of the scenarios: those of a in its order,
// then those only b has.
func scenarioRows(a, b *JSONReport) []string {
	byID := func(r *JSONReport) map[string]ScenarioReport {
		out := make(map[string]ScenarioReport, len(r.Scenarios))
		for _, sc := range r.Scenarios {
			out[sc.ID] = sc
		}

		return out
	}

	inA, inB := byID(a), byID(b)

	var ids []string

	for _, sc := range a.Scenarios {
		ids = append(ids, sc.ID)
	}

	for _, sc := range b.Scenarios {
		if _, ok := inA[sc.ID]; !ok {
			ids = append(ids, sc.ID)
		}
	}

	rows := make([]string, 0, len(ids))

	for _, id := range ids {
		sa, okA := inA[id]
		sb, okB := inB[id]
		rows = append(rows, row(id, correctCell(sa, okA), correctCell(sb, okB), rootCauseMeanCell(sa, okA), rootCauseMeanCell(sb, okB)))
	}

	return rows
}

// differenceFlag says whether the intervals of the metric key in a and b
// overlap, or - when either has none.
func differenceFlag(a, b map[string]Interval, key string) string {
	ia, okA := a[key]
	ib, okB := b[key]

	switch {
	case !okA || !okB:
		return noValue
	case ia.overlaps(ib):
		return noClearDifference
	default:
		return clearDifference
	}
}

// overlaps reports whether the two intervals share any value.
func (i Interval) overlaps(o Interval) bool {
	return i.Lower <= o.Upper && o.Lower <= i.Upper
}

// row joins cells into one table row.
func row(cells ...string) string {
	return strings.Join(cells, "\t")
}

// build is the version and commit of the Sidekick a report ran with.
func build(r *JSONReport) string {
	return fmt.Sprintf("%s (%s)", r.SidekickVersion, r.GitSHA)
}

// connectorCell is the connector and model of role, or - when it has none.
func connectorCell(connectors map[model.Role]ConnectorInfo, role model.Role) string {
	c, ok := connectors[role]
	if !ok {
		return noValue
	}

	return fmt.Sprintf("%s (%s)", c.Connector, cmpOrDash(c.Model))
}

// strategiesCell lists the strategies with their versions, by name.
func strategiesCell(strategies map[string]string) string {
	names := slices.Sorted(maps.Keys(strategies))
	if len(names) == 0 {
		return noValue
	}

	parts := make([]string, len(names))
	for i, name := range names {
		parts[i] = name + " " + strategies[name]
	}

	return strings.Join(parts, ", ")
}

// correctCell is "k/N" of the scenario, or - when the report lacks it.
func correctCell(sc ScenarioReport, ok bool) string {
	if !ok {
		return noValue
	}

	return fmt.Sprintf("%d/%d", sc.Spread.Correct, sc.Spread.Runs)
}

// rootCauseMeanCell is the mean root-cause score of the scenario, or -.
func rootCauseMeanCell(sc ScenarioReport, ok bool) string {
	if !ok {
		return noValue
	}

	return shareCell(sc.Spread.RootCauseMean)
}

// costCell formats US dollars, or - when v is nil.
func costCell(v *float64) string {
	if v == nil {
		return noValue
	}

	return fmt.Sprintf("$%.4f", *v)
}

// costChange formats b minus a in US dollars with its sign, or -.
func costChange(a, b *float64) string {
	if a == nil || b == nil {
		return noValue
	}

	d := *b - *a

	sign := "+"
	if d < 0 {
		sign, d = "-", -d
	}

	return fmt.Sprintf("%s$%.4f", sign, d)
}

// durationCell formats milliseconds as a duration, or - when v is nil.
func durationCell(v *float64) string {
	if v == nil {
		return noValue
	}

	return msCell(*v)
}

// durationChange formats b minus a, in milliseconds, as a duration with
// its sign, or -.
func durationChange(a, b *float64) string {
	if a == nil || b == nil {
		return noValue
	}

	d := time.Duration((*b - *a) * float64(time.Millisecond)).Round(time.Millisecond)
	if d >= 0 {
		return "+" + d.String()
	}

	return d.String()
}

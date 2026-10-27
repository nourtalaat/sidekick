package investigator

import (
	"cmp"
	"fmt"
	"slices"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
	"github.com/nourtalaat/sidekick/internal/store/db"
)

// maxStopDetail is the longest stop detail kept in a report, in bytes.
const maxStopDetail = 1000

// ending is how a run ends: the report, without what was checked, which
// finish adds, and the investigation's final status.
type ending struct {
	report report.Report
	// status is StatusFinal when empty.
	status string
	// budget names the budget that ran out, if one did.
	budget string
}

// noVerdict returns the ending of a run whose model gave no report that
// passed its checks.
func (r *run) noVerdict(reason, detail string) ending {
	return ending{report: r.hist.inconclusive(report.StopNoVerdict, reason, detail)}
}

// failed returns the ending of a run that could not go on.
func (r *run) failed(reason string) ending {
	return ending{report: r.hist.inconclusive(report.StopError, reason, ""), status: StatusFailed}
}

// inconclusive returns the report of a run that ended without an accepted
// report: inconclusive, with the findings that record_finding accepted.
func (h *history) inconclusive(stop report.StopReason, reason, detail string) report.Report {
	return report.Report{
		Verdict:    report.Inconclusive,
		Confidence: report.Low,
		Reason:     reason,
		Findings:   h.recordedFindings(),
		StopReason: stop,
		StopDetail: source.CutText(detail, maxStopDetail),
	}
}

// checked returns what the run checked (VR-6): one entry per source called
// in any run of the investigation, from the saved results, and the entries
// of start for sources that failed when this run started and were not
// called. A source's status is the best of its calls: ok, then no_data,
// then failed, then unavailable; Error is the message of its last failed
// call.
func (h *history) checked(start []report.Check) []report.Check {
	bySource := map[string]*report.Check{}

	for _, s := range h.steps {
		status, ok := checkStatus(s)
		if !ok {
			continue
		}

		c, seen := bySource[*s.Source]
		if !seen {
			c = &report.Check{Source: *s.Source, Status: status}
			bySource[*s.Source] = c
		}

		c.ToolCalls++

		if checkRank(status) < checkRank(c.Status) {
			c.Status = status
		}

		if s.Error != nil && (status == report.CheckFailed || status == report.CheckUnavailable) {
			c.Error = *s.Error
		}
	}

	out := make([]report.Check, 0, len(bySource)+len(start))
	for _, c := range bySource {
		out = append(out, *c)
	}

	for _, c := range start {
		if _, seen := bySource[c.Source]; !seen {
			out = append(out, c)
		}
	}

	slices.SortFunc(out, func(a, b report.Check) int { return cmp.Compare(a.Source, b.Source) })

	return out
}

// progress returns the progress lines of the saved steps, oldest first.
func (h *history) progress() []output.Progress {
	var out []output.Progress

	for _, s := range h.steps {
		if line, ok := h.progressLine(s); ok {
			out = append(out, line)
		}
	}

	return out
}

// progressLine returns the progress line of a step, if it has one: a
// source call, or a finding that record_finding accepted. The claim of a
// finding is the model's own text; the sinks escape it.
func (h *history) progressLine(s db.Step) (output.Progress, bool) {
	line := output.Progress{At: s.StartedAt}

	switch {
	case isBuiltin(s, RecordFindingTool) && s.Status == statusOK:
		f, _ := h.lastFinding(s)
		line.Text = "Finding: " + f.Claim

		if f.Hypothesis() {
			line.Text = "Hypothesis: " + f.Claim
		}
	case s.Kind == kindToolResult && s.Source != nil:
		line.Text = callLine(*s.Source, deref(s.Tool), s.Status)
	}

	return line, line.Text != ""
}

// lastFinding returns the finding a record_finding step accepted.
func (h *history) lastFinding(s db.Step) (report.Finding, bool) {
	call, ok := h.calls[deref(s.ToolCallID)]
	if !ok {
		return report.Finding{}, false
	}

	f, ok := decodeFinding(call)
	if !ok {
		return report.Finding{}, false
	}

	finding, _ := h.finding(0, f)

	return finding, true
}

// callLine is the progress line of one source call.
func callLine(sourceName, tool, status string) string {
	switch status {
	case statusOK:
		return fmt.Sprintf("Checked %s (%s)", sourceName, tool)
	case statusNoData:
		return fmt.Sprintf("Checked %s (%s): no data", sourceName, tool)
	case statusTimeout:
		return fmt.Sprintf("%s (%s) did not answer in time", sourceName, tool)
	case statusUnavailable:
		return fmt.Sprintf("%s (%s) is unavailable", sourceName, tool)
	case statusError:
		return fmt.Sprintf("%s (%s) failed", sourceName, tool)
	default:
		return ""
	}
}

// checkStatus returns the check status of a source call's result step,
// and false for any other step, including calls that never reached the
// source.
func checkStatus(s db.Step) (report.CheckStatus, bool) {
	if s.Kind != kindToolResult || s.Source == nil {
		return "", false
	}

	switch s.Status {
	case statusOK:
		return report.CheckOK, true
	case statusNoData:
		return report.CheckNoData, true
	case statusError, statusTimeout:
		return report.CheckFailed, true
	case statusUnavailable:
		return report.CheckUnavailable, true
	default:
		return "", false
	}
}

// checkRank orders check statuses, best first.
func checkRank(s report.CheckStatus) int {
	return slices.Index([]report.CheckStatus{report.CheckOK, report.CheckNoData, report.CheckFailed, report.CheckUnavailable}, s)
}

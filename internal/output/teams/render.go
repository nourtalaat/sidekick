package teams

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// What the main message shows, at most. The rest of a long report goes in
// a thread reply, which shows at most maxMore of each.
const (
	maxFindings       = 5
	maxFixes          = 3
	maxMore           = 20
	maxProgressLines  = 10
	maxLinksInFinding = 3
)

// Lengths text is cut to, in characters, so a message stays well within
// Teams' limit of about 28 KB.
const (
	maxTitleChars  = 150
	maxReasonChars = 1500
	maxFieldChars  = 600
	maxClaimChars  = 300
	maxFixChars    = 200
	maxLineChars   = 300
)

// noTitle names an event that has neither a title nor an alert name.
const noTitle = "the alert"

// The icons of the states that are not a verdict.
const (
	iconRunning = "🔍"
	iconDone    = "✅"
)

// separator joins the parts of a context line.
const separator = " · "

// verdictStyle is how a verdict looks: an icon and a label, always shown
// together with the section's color, which is never the only signal.
type verdictStyle struct {
	icon  string
	label string
	style string
}

// verdictStyles follow the design language, in the colors Teams offers.
var verdictStyles = map[report.Verdict]verdictStyle{
	report.TruePositive:  {icon: "🚨", label: "Real issue", style: styleAttention},
	report.FalsePositive: {icon: "🔕", label: "False alarm", style: styleGood},
	report.Inconclusive:  {icon: "❔", label: "Not sure yet", style: styleEmphasis},
	report.Duplicate:     {icon: "🔗", label: "Duplicate", style: styleAccent},
	report.KnownIssue:    {icon: "📌", label: "Known issue", style: styleAccent},
	report.Flapping:      {icon: "🔁", label: "Flapping", style: styleAccent},
}

// failedStyle is how a run that could not finish looks.
var failedStyle = verdictStyle{icon: "⚠️", label: "Could not finish", style: styleWarning}

// confidenceWords are the confidence levels as the message says them.
var confidenceWords = map[report.Confidence]string{
	report.Low:    "Low confidence",
	report.Medium: "Medium confidence",
	report.High:   "High confidence",
}

// mainView renders the main message for u: "Looking into it" while
// queued, the progress while running, and the report once final.
func mainView(u output.Update) activity {
	title := eventTitle(u)

	switch {
	case u.Phase == output.PhaseFinal && failed(u.Report):
		return failedView(u, title)
	case u.Phase == output.PhaseFinal:
		return finalView(u, title)
	case u.Phase == output.PhaseRunning:
		return runningView(u, title)
	default:
		return queuedView(u, title)
	}
}

// verdictReplyView renders the short thread reply that announces the
// final verdict, because editing a message notifies nobody.
func verdictReplyView(u output.Update) activity {
	title := eventTitle(u)
	style := styleOfReport(u.Report)

	return message(u, summary(u.Report, title), container(style.style, verdictLine(u.Report), line(subtle(title))))
}

// continuationView renders the findings and fixes the main message has no
// room for. It reports false when everything fit.
func continuationView(u output.Update) (activity, bool) {
	if u.Report == nil || failed(u.Report) {
		return activity{}, false
	}

	findings := u.Report.Findings
	fixes := u.Report.Fixes

	if len(findings) <= maxFindings && len(fixes) <= maxFixes {
		return activity{}, false
	}

	var body []element

	if len(findings) > maxFindings {
		body = append(body, line(bold("More findings")))
		body = append(body, findingLines(capped(findings[maxFindings:], maxMore))...)
	}

	if len(fixes) > maxFixes {
		body = append(body, line(bold("More proposed fixes")))
		body = append(body, fixLines(capped(fixes, maxFixes+maxMore)[maxFixes:], maxFixes)...)
	}

	return message(u, "More of the report: "+eventTitle(u), body...), true
}

// queuedView is the first message, posted when the investigation is
// created.
func queuedView(u output.Update, title string) activity {
	return message(u, "Looking into it: "+title,
		container(styleWarning, line(plain(iconRunning+" "), bold("Looking into it"), plain(" "+title))),
		line(subtle(runningContext(u))),
	)
}

// runningView shows the progress so far, newest last.
func runningView(u output.Update, title string) activity {
	body := []element{container(styleWarning, line(plain(iconRunning+" "), bold("Investigating"), plain(" "+title)))}

	progress := u.Progress
	if len(progress) > maxProgressLines {
		progress = progress[len(progress)-maxProgressLines:]
	}

	for _, p := range progress {
		body = append(body, line(plain(iconDone+" "+cut(p.Text, maxLineChars))))
	}

	body = append(body, line(subtle(runningContext(u))))

	return message(u, "Investigating: "+title, body...)
}

// finalView shows the report, in the order of the design: the verdict and
// confidence with the reason, the cause and impact, the findings, the
// fixes, then what was checked.
func finalView(u output.Update, title string) activity {
	r := u.Report
	style := styleOfReport(r)

	body := []element{container(style.style, verdictLine(r), line(plain(cut(r.Reason, maxReasonChars))), line(subtle(title)))}
	body = append(body, reportFields(r)...)

	if len(r.Findings) > 0 {
		body = append(body, section("Findings"))
		body = append(body, findingLines(capped(r.Findings, maxFindings))...)
	}

	if len(r.Fixes) > 0 {
		body = append(body, section("Proposed fixes"))
		body = append(body, fixLines(capped(r.Fixes, maxFixes), 0)...)
	}

	checked := checkedLine(r)
	if r.StopReason == report.StopCancelled {
		checked = joinParts(checked, "Stopped by a responder")
	}

	if checked != "" {
		body = append(body, line(subtle(checked)))
	}

	return message(u, summary(r, title), body...)
}

// failedView shows a run that stopped with an error, with what it checked.
func failedView(u output.Update, title string) activity {
	detail := "The investigation stopped with an error."
	if u.Report != nil && u.Report.StopDetail != "" {
		detail = cut(u.Report.StopDetail, maxReasonChars)
	}

	body := []element{container(failedStyle.style, verdictLine(u.Report), line(plain(detail)), line(subtle(title)))}

	if checked := checkedLine(u.Report); checked != "" {
		body = append(body, line(subtle(checked)))
	}

	return message(u, failedStyle.label+": "+title, body...)
}

// message is a card with body, and a button to the investigation in the
// web UI when there is one, with summary as its notification text.
func message(u output.Update, summary string, body ...element) activity {
	c := newCard(body...)
	if isWebURL(u.URL) {
		c.Actions = []cardLink{{Type: typeOpenURL, Title: "Open in Sidekick", URL: u.URL}}
	}

	return activity{Type: "message", Summary: cut(summary, maxTitleChars), Attachments: []cardAttachment{{ContentType: contentTypeCard, Content: c}}}
}

// verdictLine is the first line of a final message, for example "🚨 Real
// issue · High confidence". Not sure yet shows no confidence, and a failed
// run shows "Could not finish".
func verdictLine(r *report.Report) element {
	style := styleOfReport(r)
	runs := []textRun{plain(style.icon + " "), {Type: typeTextRun, Text: style.label, Weight: weightBolder, Size: sizeMedium}}

	if !failed(r) && r.Verdict != report.Inconclusive {
		if words, ok := confidenceWords[r.Confidence]; ok {
			runs = append(runs, plain(separator+words))
		}
	}

	return line(runs...)
}

// summary is the notification text of a final message.
func summary(r *report.Report, title string) string {
	style := styleOfReport(r)
	if !failed(r) && r.Verdict != report.Inconclusive {
		if words, ok := confidenceWords[r.Confidence]; ok {
			return fmt.Sprintf("%s (%s): %s", style.label, strings.ToLower(words), title)
		}
	}

	return style.label + ": " + title
}

// reportFields are the two fields under the verdict: the likely cause and
// the impact, or for a false alarm why it fired and how to stop it.
func reportFields(r *report.Report) []element {
	var out []element

	add := func(name, value string) {
		if value != "" {
			out = append(out, line(bold(name+": "), plain(cut(value, maxFieldChars))))
		}
	}

	if r.Verdict == report.FalsePositive && r.FalsePositive != nil {
		add("Why it fired", r.FalsePositive.WhyFired)

		steps := make([]string, 0, len(r.FalsePositive.Prevention))
		for _, p := range r.FalsePositive.Prevention {
			steps = append(steps, p.Detail)
		}

		add("How to stop it", strings.Join(steps, "; "))

		return out
	}

	add("Likely cause", r.Cause)
	add("Impact", r.Impact)

	return out
}

// findingLines returns one line per finding, with its evidence links. A
// finding without evidence is marked as a hypothesis.
func findingLines(findings []report.Finding) []element {
	out := make([]element, 0, len(findings))

	for _, f := range findings {
		claim := cut(f.Claim, maxClaimChars)
		if f.Hypothesis() {
			out = append(out, line(plain("• "), textRun{Type: typeTextRun, Text: "Hypothesis, no evidence yet: ", Italic: true}, plain(claim)))

			continue
		}

		runs := []textRun{plain("• " + claim)}
		for i, l := range evidenceLinks(f.Evidence) {
			sep := " "
			if i > 0 {
				sep = separator
			}

			runs = append(runs, plain(sep), l)
		}

		out = append(out, line(runs...))
	}

	return out
}

// evidenceLinks returns links to the evidence that has a usable URL, at
// most maxLinksInFinding of them.
func evidenceLinks(evidence []source.Evidence) []textRun {
	var out []textRun

	for _, e := range evidence {
		if len(out) == maxLinksInFinding {
			break
		}

		title := e.Title
		if title == "" {
			title = string(e.Kind)
		}

		if isWebURL(e.URL) {
			out = append(out, linkRun(cut(title, maxTitleChars), e.URL))
		}
	}

	return out
}

// fixLines returns the numbered titles of fixes, counting from after
// skip.
func fixLines(fixes []report.Fix, skip int) []element {
	out := make([]element, 0, len(fixes))
	for i, f := range fixes {
		out = append(out, line(plain(fmt.Sprintf("%d. %s", skip+i+1, cut(f.Title, maxFixChars)))))
	}

	return out
}

// checkedLine says which sources were checked and which did not respond,
// and whether the run stopped at its budget.
func checkedLine(r *report.Report) string {
	if r == nil {
		return ""
	}

	var checked, failedSources []string

	for _, c := range r.Checked {
		switch c.Status {
		case report.CheckOK, report.CheckNoData:
			checked = append(checked, c.Source)
		case report.CheckFailed, report.CheckUnavailable:
			failedSources = append(failedSources, c.Source)
		}
	}

	var parts []string
	if len(checked) > 0 {
		parts = append(parts, "Checked "+strings.Join(checked, ", "))
	}

	for _, s := range failedSources {
		parts = append(parts, s+" did not respond")
	}

	if r.StopReason == report.StopBudget {
		parts = append(parts, "Stopped at its budget")
	}

	return strings.Join(parts, separator)
}

// runningContext is the context line while the investigation runs: when
// the event came in, and how many events the group has.
func runningContext(u output.Update) string {
	var parts []string
	if !u.Event.ReceivedAt.IsZero() {
		parts = append(parts, "Received "+u.Event.ReceivedAt.UTC().Format(time.DateTime)+" UTC")
	}

	if u.Events > 1 {
		parts = append(parts, fmt.Sprintf("%d events", u.Events))
	}

	return strings.Join(append(parts, "Updates as the investigation runs"), separator)
}

// section is the heading of a part of the report.
func section(name string) element {
	e := line(bold(name))
	e.Spacing = spacingMed
	e.Separator = true

	return e
}

// joinParts joins the parts of a context line that are not empty.
func joinParts(parts ...string) string {
	kept := make([]string, 0, len(parts))

	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}

	return strings.Join(kept, separator)
}

// eventTitle is the event's title, its alert name, or noTitle, cut to
// maxTitleChars.
func eventTitle(u output.Update) string {
	title := u.Event.Title
	if title == "" {
		title = u.Event.AlertName
	}

	if title == "" {
		title = noTitle
	}

	return cut(title, maxTitleChars)
}

// failed reports whether a final update is for a run that could not
// finish: it has no report, or the run stopped with an error.
func failed(r *report.Report) bool {
	return r == nil || r.StopReason == report.StopError
}

// styleOfReport returns the style of r: failedStyle for a run that could
// not finish, else its verdict's, or Not sure yet for a verdict it does
// not know.
func styleOfReport(r *report.Report) verdictStyle {
	if failed(r) {
		return failedStyle
	}

	if s, ok := verdictStyles[r.Verdict]; ok {
		return s
	}

	return verdictStyles[report.Inconclusive]
}

// capped returns at most limit items of s.
func capped[T any](s []T, limit int) []T {
	if len(s) > limit {
		return s[:limit]
	}

	return s
}

// cut returns s with at most limit characters, ending with an ellipsis
// when it was longer. Surrounding white space is dropped.
func cut(s string, limit int) string {
	s = strings.TrimSpace(s)

	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}

	return strings.TrimSpace(string(runes[:limit-1])) + "…"
}

// isWebURL reports whether raw is an absolute http or https URL.
func isWebURL(raw string) bool {
	u, err := url.Parse(raw)

	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

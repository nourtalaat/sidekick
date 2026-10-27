package ticket

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// What a comment shows, at most.
const (
	maxFindings       = 8
	maxFixes          = 5
	maxLinksInFinding = 3
)

// Lengths text is cut to, in bytes, so a whole comment stays well below
// the smallest limit of the ticket systems (Jira's 32,767 characters).
const (
	maxTitleBytes     = 200
	maxReasonBytes    = 3000
	maxFieldBytes     = 1500
	maxClaimBytes     = 400
	maxLinkTitleBytes = 100
	maxFixBytes       = 300
	maxFixDetailBytes = 500
)

// ellipsis ends text that was cut.
const ellipsis = "…"

// noTitle names an event that has neither a title nor an alert name.
const noTitle = "the alert"

// Doc is a comment in a form every ticket markup can render: blocks of
// text spans. Span text is plain, untrusted text that each renderer
// escapes; only Span.URL makes a link.
type Doc struct {
	Blocks []Block
}

// BlockKind is how a block is laid out.
type BlockKind string

// Block kinds.
const (
	// Paragraph is one paragraph: its single line.
	Paragraph BlockKind = "paragraph"
	// Bullets is a list of its lines.
	Bullets BlockKind = "bullets"
	// Numbered is a numbered list of its lines.
	Numbered BlockKind = "numbered"
)

// Block is a paragraph or a list, with an optional bold heading line.
type Block struct {
	Kind    BlockKind
	Heading string
	Lines   []Line
}

// Line is one line of text, made of spans.
type Line []Span

// Span is a run of text with one style.
type Span struct {
	Text   string
	Bold   bool
	Italic bool
	// URL makes the span a link. It is always an absolute http or https
	// URL from evidence or Sidekick itself, never from model text.
	URL string
}

// verdictLabels are the verdicts as the comment says them, with the same
// labels and emoji as the Slack output.
var verdictLabels = map[report.Verdict]string{
	report.TruePositive:  "🚨 Real issue",
	report.FalsePositive: "🔕 False alarm",
	report.Inconclusive:  "❔ Not sure yet",
	report.Duplicate:     "🔗 Duplicate",
	report.KnownIssue:    "📌 Known issue",
	report.Flapping:      "🔁 Flapping",
}

// failedLabel is the label of a run that could not finish.
const failedLabel = "⚠️ Could not finish"

// confidenceWords are the confidence levels as the comment says them.
var confidenceWords = map[report.Confidence]string{
	report.Low:    "Low confidence",
	report.Medium: "Medium confidence",
	report.High:   "High confidence",
}

// Layout lays out the comment of a final update: the verdict and
// confidence with the reason, the cause and impact (or, for a false alarm,
// why it fired and how to stop it), the findings with their evidence
// links, the proposed fixes, what was checked, and a link to the
// investigation page when there is one. A run that could not finish shows
// that, with what it checked.
func Layout(u output.Update) Doc {
	var blocks []Block

	if failed(u.Report) {
		blocks = failedBlocks(u)
	} else {
		blocks = reportBlocks(u)
	}

	if checked := checkedText(u.Report); checked != "" {
		blocks = append(blocks, paragraph(Span{Text: checked, Italic: true}))
	}

	if l := link(u.URL, "Open the investigation in Sidekick"); l.URL != "" {
		blocks = append(blocks, paragraph(l))
	}

	return Doc{Blocks: blocks}
}

// failedBlocks are the blocks of a run that stopped with an error.
func failedBlocks(u output.Update) []Block {
	detail := "The investigation stopped with an error."
	if u.Report != nil && u.Report.StopDetail != "" {
		detail = cut(u.Report.StopDetail, maxReasonBytes)
	}

	return []Block{
		paragraph(Span{Text: failedLabel, Bold: true}, Span{Text: " · Sidekick investigated " + eventTitle(u)}),
		paragraph(Span{Text: detail}),
	}
}

// reportBlocks are the blocks of a report, in the order of the Slack
// message.
func reportBlocks(u output.Update) []Block {
	r := u.Report

	headline := []Span{{Text: verdictLabel(r.Verdict), Bold: true}}
	if words, ok := confidenceWords[r.Confidence]; ok && r.Verdict != report.Inconclusive {
		headline = append(headline, Span{Text: " · " + words})
	}

	about := " · Sidekick investigated " + eventTitle(u)
	if u.Events > 1 {
		about += fmt.Sprintf(" (%d events)", u.Events)
	}

	blocks := []Block{
		paragraph(append(headline, Span{Text: about})...),
		paragraph(Span{Text: cut(r.Reason, maxReasonBytes)}),
	}

	blocks = append(blocks, fieldBlocks(r)...)

	if lines := findingLines(r.Findings); len(lines) > 0 {
		blocks = append(blocks, Block{Kind: Bullets, Heading: "Findings", Lines: lines})
	}

	if lines := fixLines(r.Fixes); len(lines) > 0 {
		blocks = append(blocks, Block{Kind: Numbered, Heading: "Proposed fixes", Lines: lines})
	}

	return blocks
}

// fieldBlocks are the likely cause and the impact, or for a false alarm
// why it fired and how to stop it.
func fieldBlocks(r *report.Report) []Block {
	var blocks []Block

	add := func(heading, value string) {
		if value = cut(value, maxFieldBytes); value != "" {
			blocks = append(blocks, Block{Kind: Paragraph, Heading: heading, Lines: []Line{{{Text: value}}}})
		}
	}

	if r.Verdict == report.FalsePositive && r.FalsePositive != nil {
		add("Why it fired", r.FalsePositive.WhyFired)

		var steps []Line
		for _, p := range r.FalsePositive.Prevention {
			if detail := cut(p.Detail, maxFieldBytes); detail != "" {
				steps = append(steps, Line{{Text: detail}})
			}
		}

		if len(steps) > 0 {
			blocks = append(blocks, Block{Kind: Bullets, Heading: "How to stop it", Lines: steps})
		}

		return blocks
	}

	add("Likely cause", r.Cause)
	add("Impact", r.Impact)

	return blocks
}

// findingLines are the first maxFindings findings with their evidence
// links. A finding without evidence is marked as a hypothesis.
func findingLines(findings []report.Finding) []Line {
	lines := make([]Line, 0, min(len(findings), maxFindings+1))

	for i, f := range findings {
		if i == maxFindings {
			lines = append(lines, Line{{Text: fmt.Sprintf("%d more in the investigation", len(findings)-maxFindings), Italic: true}})

			break
		}

		claim := Span{Text: cut(f.Claim, maxClaimBytes)}
		if f.Hypothesis() {
			lines = append(lines, Line{{Text: "Hypothesis, no evidence yet: ", Italic: true}, claim})

			continue
		}

		line := Line{claim}
		for _, l := range evidenceLinks(f.Evidence) {
			line = append(line, Span{Text: " · "}, l)
		}

		lines = append(lines, line)
	}

	return lines
}

// evidenceLinks are links to the evidence that has a usable URL, at most
// maxLinksInFinding of them.
func evidenceLinks(evidence []source.Evidence) []Span {
	var links []Span

	for _, e := range evidence {
		if len(links) == maxLinksInFinding {
			break
		}

		title := e.Title
		if title == "" {
			title = string(e.Kind)
		}

		if l := link(e.URL, cut(title, maxLinkTitleBytes)); l.URL != "" {
			links = append(links, l)
		}
	}

	return links
}

// fixLines are the first maxFixes fixes, each a bold title and its
// detail.
func fixLines(fixes []report.Fix) []Line {
	lines := make([]Line, 0, min(len(fixes), maxFixes))

	for _, f := range fixes[:min(len(fixes), maxFixes)] {
		line := Line{{Text: cut(f.Title, maxFixBytes), Bold: true}}
		if detail := cut(f.Detail, maxFixDetailBytes); detail != "" {
			line = append(line, Span{Text: ": " + detail})
		}

		lines = append(lines, line)
	}

	return lines
}

// checkedText says which sources were checked and which did not respond,
// and whether the run stopped at its budget.
func checkedText(r *report.Report) string {
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

	return strings.Join(parts, " · ")
}

// eventTitle is the event's title, its alert name, or noTitle, cut to
// maxTitleBytes, on one line.
func eventTitle(u output.Update) string {
	title := u.Event.Title
	if title == "" {
		title = u.Event.AlertName
	}

	if title = cut(strings.Join(strings.Fields(title), " "), maxTitleBytes); title == "" {
		return noTitle
	}

	return title
}

// verdictLabel returns the label of v, or the Not sure yet label for a
// verdict it does not know.
func verdictLabel(v report.Verdict) string {
	if l, ok := verdictLabels[v]; ok {
		return l
	}

	return verdictLabels[report.Inconclusive]
}

// failed reports whether a final update is for a run that could not
// finish: it has no report, or the run stopped with an error.
func failed(r *report.Report) bool {
	return r == nil || r.StopReason == report.StopError
}

// paragraph returns a paragraph of one line.
func paragraph(spans ...Span) Block {
	return Block{Kind: Paragraph, Lines: []Line{spans}}
}

// link returns a span that links to rawURL with the text title, or a span
// with no URL when rawURL is not an absolute http or https URL.
func link(rawURL, title string) Span {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return Span{}
	}

	return Span{Text: title, URL: u.String()}
}

// cut returns s without surrounding white space, cut to at most limit
// bytes on a character boundary and ending with an ellipsis when it was
// longer.
func cut(s string, limit int) string {
	s = strings.TrimSpace(s)
	if len(s) <= limit {
		return s
	}

	return strings.TrimSpace(source.CutText(s, limit-len(ellipsis))) + ellipsis
}

package slack

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/nourtalaat/sidekick/internal/output"
	"github.com/nourtalaat/sidekick/internal/report"
	"github.com/nourtalaat/sidekick/internal/source"
)

// Bar colors that are not a verdict's.
const (
	// colorRunning is brand orange, the bar while an investigation runs.
	colorRunning = "#FF8848"
	// colorFailed is the warning yellow of a run that could not finish.
	colorFailed = "#B08400"
)

// What the main message shows, at most. The rest of a long report goes in
// a thread reply.
const (
	maxFindings       = 5
	maxFixes          = 3
	maxProgressLines  = 10
	maxLinksInFinding = 3
)

// Lengths text is cut to, in characters, so every block stays within
// Slack's limits (3000 characters for a section, 2000 for a field).
const (
	maxTitleChars   = 150
	maxReasonChars  = 1500
	maxFieldChars   = 600
	maxClaimChars   = 300
	maxFixChars     = 200
	maxLineChars    = 300
	maxSectionChars = 2900
)

// noTitle names an event that has neither a title nor an alert name.
const noTitle = "the alert"

// The action IDs of the buttons on the main message. Each button's value
// is the investigation ID; the Slack app reads both back from a click.
const (
	ActionVerdictCorrect = "verdict_correct"
	ActionVerdictWrong   = "verdict_wrong"
	ActionStop           = "stop_investigation"
)

// The block IDs of the button rows.
const (
	blockIDFeedback = "feedback"
	blockIDRun      = "run"
)

// slackUserID is a Slack user ID. Only IDs of this form are shown as
// mentions, so a stored value can never add other markup.
var slackUserID = regexp.MustCompile(`^[UW][A-Z0-9]{2,}$`)

// verdictStyle is how a verdict looks: an emoji and a label, always shown
// together with the bar color, which is never the only signal.
type verdictStyle struct {
	emoji string
	label string
	color string
}

// verdictStyles follow the design language. Duplicate and Known issue
// share the blue, told apart by their emoji and labels.
var verdictStyles = map[report.Verdict]verdictStyle{
	report.TruePositive:  {emoji: ":rotating_light:", label: "Real issue", color: "#C4261C"},
	report.FalsePositive: {emoji: ":no_bell:", label: "False alarm", color: "#1F8A4C"},
	report.Inconclusive:  {emoji: ":grey_question:", label: "Not sure yet", color: "#7C85A3"},
	report.Duplicate:     {emoji: ":link:", label: "Duplicate", color: "#2C57CD"},
	report.KnownIssue:    {emoji: ":pushpin:", label: "Known issue", color: "#2C57CD"},
	report.Flapping:      {emoji: ":repeat:", label: "Flapping", color: "#17998D"},
}

// failedStyle is how a run that could not finish looks.
var failedStyle = verdictStyle{emoji: ":warning:", label: "Could not finish", color: colorFailed}

// confidenceWords are the confidence levels as the message says them.
var confidenceWords = map[report.Confidence]string{
	report.Low:    "Low confidence",
	report.Medium: "Medium confidence",
	report.High:   "High confidence",
}

// view is one message to post or edit: the notification text and the
// attachment, both escaped already.
type view struct {
	text       string
	attachment attachment
}

// mainView renders the main message for u: "Looking into it" while
// queued, the progress while running, and the report once final, also
// after a follow-up answer.
func mainView(u output.Update) view {
	title := eventTitle(u)

	ended := u.Phase == output.PhaseFinal || u.Phase == output.PhaseFollowUp

	switch {
	case ended && failed(u.Report):
		return failedView(u, title)
	case ended:
		return finalView(u, title)
	case u.Phase == output.PhaseRunning:
		return runningView(u, title)
	default:
		return queuedView(u, title)
	}
}

// verdictReplyView renders the short thread reply that announces the
// final verdict, linking to the main message when permalink is set.
func verdictReplyView(u output.Update, permalink string) view {
	title := eventTitle(u)
	style, line := verdictLine(u.Report)

	if permalink != "" {
		line += " · " + link(permalink, "See the full report")
	}

	return view{
		text: escape(notificationText(u.Report, title)),
		attachment: attachment{Color: style.color, Blocks: []block{
			section(line),
			contextBlock(escape(title)),
		}},
	}
}

// continuationView renders the findings and fixes the main message has no
// room for. It reports false when everything fit.
func continuationView(u output.Update) (view, bool) {
	if u.Report == nil || failed(u.Report) {
		return view{}, false
	}

	_, moreFindings := findingLines(u.Report.Findings)
	_, moreFixes := fixLines(u.Report.Fixes)

	if len(moreFindings) == 0 && len(moreFixes) == 0 {
		return view{}, false
	}

	var blocks []block
	blocks = append(blocks, chunked("*More findings*", moreFindings)...)
	blocks = append(blocks, chunked("*More proposed fixes*", moreFixes)...)

	style, _ := verdictLine(u.Report)

	return view{
		text:       escape("More of the report: " + eventTitle(u)),
		attachment: attachment{Color: style.color, Blocks: blocks},
	}, true
}

// answerView renders the thread reply with one follow-up answer: who
// asked, the answer with links to its evidence, and, when the run changed
// the verdict, the old and the new verdict. An answer the run could not
// give shows why, with the warning style.
func answerView(u output.Update, f report.FollowUp) view {
	style := styleOf(u.Report.Verdict)
	heading := ":speech_balloon: *Answer*"

	if !f.Answered {
		style, heading = failedStyle, failedStyle.emoji+" *No answer*"
	}

	if askers := askerNames(f.Questions); askers != "" {
		heading += " for " + askers
	}

	text := escape(cut(f.Answer, maxReasonChars))
	if links := evidenceLinks(f.Evidence); len(links) > 0 {
		text += "\n" + strings.Join(links, " · ")
	}

	blocks := []block{section(heading + "\n" + text)}

	if f.VerdictChanged() {
		_, now := verdictLine(u.Report)
		was := styleOf(f.PreviousVerdict)
		blocks = append(blocks, contextBlock("Verdict changed from "+was.emoji+" "+was.label+" to "+now))
	}

	return view{
		text:       escape("Answer: " + cut(f.Answer, maxTitleChars)),
		attachment: attachment{Color: style.color, Blocks: blocks},
	}
}

// askerNames returns who asked the questions, each once, in the order
// they asked, escaped: their names, or a mention of a Slack user known
// only by ID, which also lets them know of the answer.
func askerNames(questions []report.FollowUpQuestion) string {
	var names []string

	for _, q := range questions {
		name := escape(q.Author)
		if name == "" && slackUserID.MatchString(q.AuthorID) {
			name = "<@" + q.AuthorID + ">"
		}

		if name != "" && !slices.Contains(names, name) {
			names = append(names, name)
		}
	}

	return strings.Join(names, ", ")
}

// queuedView is the first message, posted when the investigation is
// created.
func queuedView(u output.Update, title string) view {
	return view{
		text: escape("Looking into it: " + title),
		attachment: attachment{Color: colorRunning, Blocks: slices.Concat([]block{
			section("*Looking into it* " + escape(title)),
			contextBlock(runningContext(u)),
		}, watcherContext(u), []block{stopButtons(u)}, watcherButtons(u))},
	}
}

// runningView shows the progress so far, newest last.
func runningView(u output.Update, title string) view {
	blocks := []block{section("*Investigating* " + escape(title))}

	progress := u.Progress
	if len(progress) > maxProgressLines {
		progress = progress[len(progress)-maxProgressLines:]
	}

	if len(progress) > 0 {
		lines := make([]string, 0, len(progress))
		for _, p := range progress {
			lines = append(lines, ":white_check_mark: "+escape(cut(p.Text, maxLineChars)))
		}

		blocks = append(blocks, section(strings.Join(lines, "\n")))
	}

	blocks = append(blocks, contextBlock(runningContext(u)))
	blocks = slices.Concat(blocks, watcherContext(u), []block{stopButtons(u)}, watcherButtons(u))

	return view{
		text:       escape("Investigating: " + title),
		attachment: attachment{Color: colorRunning, Blocks: blocks},
	}
}

// finalView shows the report, in the order of the design: the verdict and
// confidence with the reason, the cause and impact, the findings, the
// fixes, what was checked, the watchers, who rated the verdict, then the
// Correct and Wrong buttons and the Stop watching buttons. A run a responder stopped says who stopped it instead of
// asking for a rating.
func finalView(u output.Update, title string) view {
	r := u.Report
	style, line := verdictLine(r)

	blocks := []block{section(line + "\n" + escape(cut(r.Reason, maxReasonChars)))}

	if fields := reportFields(r); len(fields) > 0 {
		blocks = append(blocks, block{Type: blockSection, Fields: fields})
	}

	if findings, _ := findingLines(r.Findings); len(findings) > 0 {
		blocks = append(blocks, section("*Findings*\n"+strings.Join(findings, "\n")))
	}

	if fixes, _ := fixLines(r.Fixes); len(fixes) > 0 {
		blocks = append(blocks, section("*Proposed fixes*\n"+strings.Join(fixes, "\n")))
	}

	checked := checkedLine(r)
	if r.StopReason == report.StopCancelled {
		checked = joinParts(checked, stoppedLine(u.StoppedBy))
	}

	if checked != "" {
		blocks = append(blocks, contextBlock(checked))
	}

	blocks = append(blocks, watcherContext(u)...)

	if r.StopReason != report.StopCancelled {
		if line := feedbackLine(u.Feedback); line != "" {
			blocks = append(blocks, contextBlock(line))
		}

		blocks = append(blocks, feedbackButtons(u))
	}

	blocks = append(blocks, watcherButtons(u)...)

	return view{
		text:       escape(notificationText(r, title)),
		attachment: attachment{Color: style.color, Blocks: blocks},
	}
}

// failedView shows a run that stopped with an error, with what it checked.
func failedView(u output.Update, title string) view {
	detail := "The investigation stopped with an error."
	if u.Report != nil && u.Report.StopDetail != "" {
		detail = escape(cut(u.Report.StopDetail, maxReasonChars))
	}

	blocks := []block{section(failedStyle.emoji + " *" + failedStyle.label + "*\n" + detail)}

	if checked := checkedLine(u.Report); checked != "" {
		blocks = append(blocks, contextBlock(checked))
	}

	blocks = slices.Concat(blocks, watcherContext(u), watcherButtons(u))

	return view{
		text:       escape(failedStyle.label + ": " + title),
		attachment: attachment{Color: failedStyle.color, Blocks: blocks},
	}
}

// verdictLine returns the style of r and its first line, for example
// ":rotating_light: *Real issue* · High confidence". Not sure yet shows no
// confidence, and a failed run shows "Could not finish".
func verdictLine(r *report.Report) (verdictStyle, string) {
	if failed(r) {
		return failedStyle, failedStyle.emoji + " *" + failedStyle.label + "*"
	}

	style := styleOf(r.Verdict)
	line := style.emoji + " *" + style.label + "*"

	if words, ok := confidenceWords[r.Confidence]; ok && r.Verdict != report.Inconclusive {
		line += " · " + words
	}

	return style, line
}

// notificationText is the top-level text of a final message, which Slack
// shows in notifications and reads to screen readers.
func notificationText(r *report.Report, title string) string {
	if failed(r) {
		return failedStyle.label + ": " + title
	}

	style := styleOf(r.Verdict)
	if words, ok := confidenceWords[r.Confidence]; ok && r.Verdict != report.Inconclusive {
		return fmt.Sprintf("%s (%s): %s", style.label, strings.ToLower(words), title)
	}

	return style.label + ": " + title
}

// reportFields are the two fields under the verdict: the likely cause and
// the impact, or for a false alarm why it fired and how to stop it.
func reportFields(r *report.Report) []textObject {
	var fields []textObject

	add := func(name, value string) {
		if value != "" {
			fields = append(fields, mrkdwn("*"+name+"*\n"+escape(cut(value, maxFieldChars))))
		}
	}

	if r.Verdict == report.FalsePositive && r.FalsePositive != nil {
		add("Why it fired", r.FalsePositive.WhyFired)

		steps := make([]string, 0, len(r.FalsePositive.Prevention))
		for _, p := range r.FalsePositive.Prevention {
			steps = append(steps, p.Detail)
		}

		add("How to stop it", strings.Join(steps, "\n"))

		return fields
	}

	add("Likely cause", r.Cause)
	add("Impact", r.Impact)

	return fields
}

// findingLines returns the lines of the first maxFindings findings that
// fit in one section, and the lines of the rest.
func findingLines(findings []report.Finding) (shown, rest []string) {
	lines := make([]string, 0, len(findings))
	for _, f := range findings {
		lines = append(lines, findingLine(f))
	}

	return split(lines, maxFindings, "*Findings*\n")
}

// findingLine is one finding with its evidence links. A finding without
// evidence is marked as a hypothesis.
func findingLine(f report.Finding) string {
	claim := escape(cut(f.Claim, maxClaimChars))
	if f.Hypothesis() {
		return "• _Hypothesis, no evidence yet:_ " + claim
	}

	links := evidenceLinks(f.Evidence)
	if len(links) == 0 {
		return "• " + claim
	}

	return "• " + claim + " " + strings.Join(links, " · ")
}

// evidenceLinks returns links to the evidence that has a usable URL, at
// most maxLinksInFinding of them.
func evidenceLinks(evidence []source.Evidence) []string {
	var links []string

	for _, e := range evidence {
		if len(links) == maxLinksInFinding {
			break
		}

		title := e.Title
		if title == "" {
			title = string(e.Kind)
		}

		if l := link(e.URL, cut(title, maxTitleChars)); l != "" {
			links = append(links, l)
		}
	}

	return links
}

// fixLines returns the numbered titles of the first maxFixes fixes that
// fit in one section, and those of the rest.
func fixLines(fixes []report.Fix) (shown, rest []string) {
	lines := make([]string, 0, len(fixes))
	for i, f := range fixes {
		lines = append(lines, fmt.Sprintf("%d. %s", i+1, escape(cut(f.Title, maxFixChars))))
	}

	return split(lines, maxFixes, "*Proposed fixes*\n")
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
			checked = append(checked, escape(c.Source))
		case report.CheckFailed, report.CheckUnavailable:
			failedSources = append(failedSources, escape(c.Source))
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

// stopButtons is the row with the Stop button, which asks before it stops
// the run.
func stopButtons(u output.Update) block {
	return block{Type: blockActions, BlockID: blockIDRun, Buttons: []button{{
		Type:     elemButton,
		ActionID: ActionStop,
		Text:     plain("Stop"),
		Value:    u.InvestigationID,
		Style:    styleDanger,
		Confirm: &confirmDialog{
			Title:   plain("Stop the investigation?"),
			Text:    plain("Sidekick stops now and reports what it found so far."),
			Confirm: plain("Stop"),
			Deny:    plain("Keep going"),
			Style:   styleDanger,
		},
	}}}
}

// feedbackButtons is the row with the Correct and Wrong buttons.
func feedbackButtons(u output.Update) block {
	return block{Type: blockActions, BlockID: blockIDFeedback, Buttons: []button{
		{Type: elemButton, ActionID: ActionVerdictCorrect, Text: plain(":thumbsup: Correct"), Value: u.InvestigationID},
		{Type: elemButton, ActionID: ActionVerdictWrong, Text: plain(":thumbsdown: Wrong"), Value: u.InvestigationID},
	}}
}

// feedbackLine says who rated the verdict, such as ":thumbsup: Marked
// correct by <@U1>, <@U2> · :thumbsdown: Marked wrong by <@U3>". It is
// empty when nobody has.
func feedbackLine(feedback []output.Feedback) string {
	var correct, wrong []string

	for _, f := range feedback {
		who := mention(f.By)
		if who == "" {
			continue
		}

		switch f.Rating {
		case output.RatingCorrect:
			correct = append(correct, who)
		case output.RatingWrong:
			wrong = append(wrong, who)
		}
	}

	var parts []string
	if len(correct) > 0 {
		parts = append(parts, ":thumbsup: Marked correct by "+strings.Join(correct, ", "))
	}

	if len(wrong) > 0 {
		parts = append(parts, ":thumbsdown: Marked wrong by "+strings.Join(wrong, ", "))
	}

	return strings.Join(parts, " · ")
}

// stoppedLine says who stopped the run.
func stoppedLine(by *output.Person) string {
	if by != nil {
		if who := mention(*by); who != "" {
			return "Stopped by " + who
		}
	}

	return "Stopped by a responder"
}

// mention is the Slack mention of p, or "" when p has no valid Slack user
// ID.
func mention(p output.Person) string {
	if !slackUserID.MatchString(p.SlackUserID) {
		return ""
	}

	return "<@" + p.SlackUserID + ">"
}

// joinParts joins the parts of a context line that are not empty.
func joinParts(parts ...string) string {
	kept := make([]string, 0, len(parts))

	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}

	return strings.Join(kept, " · ")
}

// runningContext is the context line while the investigation runs: when
// the event came in, and how many events the group has.
func runningContext(u output.Update) string {
	parts := []string{}
	if !u.Event.ReceivedAt.IsZero() {
		parts = append(parts, "Received "+slackTime(u.Event.ReceivedAt))
	}

	if u.Events > 1 {
		parts = append(parts, fmt.Sprintf("%d events", u.Events))
	}

	return strings.Join(append(parts, "Updates as the investigation runs"), " · ")
}

// eventTitle is the event's title, its alert name, or noTitle, cut to
// maxTitleChars. It is not escaped.
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

// styleOf returns the style of v, or the Not sure yet style for a verdict
// it does not know.
func styleOf(v report.Verdict) verdictStyle {
	if s, ok := verdictStyles[v]; ok {
		return s
	}

	return verdictStyles[report.Inconclusive]
}

// split keeps the first limit lines that fit, after header, in one
// section, and returns the rest.
func split(lines []string, limit int, header string) (shown, rest []string) {
	size := len(header)

	for i, line := range lines {
		size += len(line) + 1
		if i == limit || size > maxSectionChars {
			return lines[:i], lines[i:]
		}
	}

	return lines, nil
}

// chunked puts lines into as many sections as they need, the first one
// starting with header.
func chunked(header string, lines []string) []block {
	var blocks []block

	prefix := header + "\n"

	for len(lines) > 0 {
		shown, rest := split(lines, len(lines), prefix)
		if len(shown) == 0 {
			shown, rest = lines[:1], lines[1:]
		}

		blocks = append(blocks, section(prefix+strings.Join(shown, "\n")))
		prefix, lines = "", rest
	}

	return blocks
}

// slackTime shows t in each reader's own time zone, with the UTC time as
// the fallback text.
func slackTime(t time.Time) string {
	return fmt.Sprintf("<!date^%d^{time}|%s UTC>", t.Unix(), t.UTC().Format("15:04"))
}

// section returns a section block with text, which must be escaped.
func section(text string) block {
	t := mrkdwn(text)

	return block{Type: blockSection, Text: &t}
}

// contextBlock returns a context block with one text, which must be
// escaped.
func contextBlock(text string) block {
	return block{Type: blockContext, Elements: []textObject{mrkdwn(text)}}
}

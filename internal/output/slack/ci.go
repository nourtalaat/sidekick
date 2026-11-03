package slack

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/nourtalaat/sidekick/internal/output"
)

// What the CI reply shows, at most: failed checks, and characters of the
// name of one.
const (
	maxFailedChecksShown = 10
	maxCheckNameChars    = 80
)

// Bar colors of the CI replies: passed is the success green. A failed run
// takes the verdict red, and every other state the warning yellow or the
// neutral gray.
const (
	colorCIPassed  = "#1F8A4C"
	colorCIFailed  = "#C4261C"
	colorCINeutral = "#7C85A3"
)

// ciStyle is how the reply of one CI state looks: an emoji, a bold title
// and the bar color, always shown together.
type ciStyle struct {
	emoji string
	title string
	color string
}

var ciStyles = map[string]ciStyle{
	output.CIPassed:   {emoji: ":white_check_mark:", title: "CI passed", color: colorCIPassed},
	output.CIFailed:   {emoji: ":x:", title: "CI failed", color: colorCIFailed},
	output.CINone:     {emoji: ":grey_question:", title: "No CI ran", color: colorCINeutral},
	output.CIUnknown:  {emoji: ":warning:", title: "CI could not be read", color: colorFailed},
	output.CITimedOut: {emoji: ":hourglass:", title: "CI had not finished", color: colorFailed},
}

// ciReported posts the thread reply about the CI result in u and returns
// the ref of the main message as it was. The publish job sends each result
// once. Without a message to reply under, it posts the message first, as
// for a queued investigation. An update without a result posts nothing.
func (s *Sink) ciReported(ctx context.Context, u output.Update) (output.Ref, error) {
	if u.CI == nil {
		s.log.WarnContext(ctx, "CI update dropped: it has no result", "investigation_id", u.InvestigationID, "seq", u.Seq)

		if u.Previous == nil {
			return output.Ref{Sink: s.name}, nil
		}

		return *u.Previous, nil
	}

	r, err := s.messageToReplyUnder(ctx, u)
	if err != nil {
		return output.Ref{}, fmt.Errorf("find the message to reply under: %w", err)
	}

	permalink, err := s.client.permalink(ctx, r.channel, r.ts)
	if err != nil {
		// The reply is still worth posting without its link.
		s.log.WarnContext(ctx, "could not get a link to the Slack message", "investigation_id", u.InvestigationID, "error", err)
	}

	if err := s.reply(ctx, r, ciReplyView(*u.CI, permalink)); err != nil {
		return output.Ref{}, fmt.Errorf("post the CI reply: %w", err)
	}

	return s.outputRef(r), nil
}

// ciReplyView renders the short thread reply about a CI result, linking to
// the main message when permalink is set. Everything the host named, the
// pull request and each check, is escaped, cut and never markup.
func ciReplyView(c output.CIReport, permalink string) view {
	style, ok := ciStyles[c.State]
	if !ok {
		style = ciStyle{emoji: ":grey_question:", title: "CI result", color: colorCINeutral}
	}

	pr := pullRequestName(c)
	head := style.emoji + " *" + style.title + "*"

	if pr != "" {
		head += " on " + pr
	}

	lines := []string{head, ciSentence(c)}
	lines = append(lines, failedLines(c)...)

	return view{
		text: escape(style.title + ciPlainSuffix(c)),
		attachment: attachment{Color: style.color, Blocks: []block{
			section(strings.Join(lines, "\n")),
			contextBlock(joinParts(link(permalink, "See the investigation"))),
		}},
	}
}

// ciSentence is the one sentence under the title that says what was found.
func ciSentence(c output.CIReport) string {
	switch c.State {
	case output.CIPassed:
		if c.Total == 1 {
			return "The check passed."
		}

		return "All " + checks(c.Total) + " passed."
	case output.CIFailed:
		return fmt.Sprintf("%d of %s failed:", c.FailedCount, checks(c.Total))
	case output.CINone:
		return "No check or pipeline started within " + waited(c.WaitedSeconds) + ". The repository may have no CI for this branch."
	case output.CITimedOut:
		return fmt.Sprintf("After %s, %d of %s had passed and %d were still running.", waited(c.WaitedSeconds), c.Passed, checks(c.Total), c.Total-c.Passed-c.FailedCount)
	default:
		return escape(cut(c.Message, maxReasonChars))
	}
}

// failedLines is one bullet for each failed check shown, and a line for the
// rest. A name is plain text; its link is kept only for an http or https
// address.
func failedLines(c output.CIReport) []string {
	if c.State != output.CIFailed {
		return nil
	}

	shown := c.Failed[:min(len(c.Failed), maxFailedChecksShown)]

	lines := make([]string, 0, len(shown)+1)

	for _, f := range shown {
		name := plainCheckName(f.Name)

		if l := link(f.URL, name); l != "" {
			lines = append(lines, "• "+l)

			continue
		}

		lines = append(lines, "• "+escape(name))
	}

	if more := c.FailedCount - len(shown); more > 0 {
		lines = append(lines, fmt.Sprintf("and %d more", more))
	}

	return lines
}

// pullRequestName is "acme/monitors #12", linked when the address is http or
// https, or "" when the report does not name the request.
func pullRequestName(c output.CIReport) string {
	if c.Repo == "" || c.Number == 0 {
		return ""
	}

	name := cut(c.Repo, maxTitleChars) + " #" + strconv.FormatInt(c.Number, 10)

	if l := link(c.URL, name); l != "" {
		return l
	}

	return escape(name)
}

// ciPlainSuffix is the notification text after the title: the request, when
// the report names it.
func ciPlainSuffix(c output.CIReport) string {
	if c.Repo == "" || c.Number == 0 {
		return ""
	}

	return " on " + cut(c.Repo, maxTitleChars) + " #" + strconv.FormatInt(c.Number, 10)
}

// plainCheckName cuts a check name and takes out the characters Slack would
// read as formatting in the text of a link.
func plainCheckName(name string) string {
	return cut(formattingRemover.Replace(name), maxCheckNameChars)
}

// formattingRemover drops the mrkdwn characters that bold, italicise, strike
// or quote the text around them.
var formattingRemover = strings.NewReplacer("*", "", "_", "", "~", "", "`", "")

// waited is a duration in seconds as a person says it, such as "10 minutes"
// or "2 hours".
func waited(seconds int) string {
	switch {
	case seconds >= secondsPerHour && seconds%secondsPerHour == 0:
		return plural(seconds/secondsPerHour, "hour")
	default:
		return plural(max(1, (seconds+secondsPerMinute-1)/secondsPerMinute), "minute")
	}
}

// plural is n and noun, with an s when n is not 1.
func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}

	return fmt.Sprintf("%d %ss", n, noun)
}

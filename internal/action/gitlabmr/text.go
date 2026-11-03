package gitlabmr

import (
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action/gitchange"
	"github.com/nourtalaat/sidekick/internal/source"
)

// draftMarker starts the title of a draft merge request. GitLab has no
// parameter for a draft: a merge request is a draft when its title starts
// with "Draft:", "[Draft]" or "(Draft)", in any case.
const draftMarker = "Draft: "

// Limits of the text sent to GitLab.
const (
	// maxCommitTitleBytes is the longest first line of the commit message.
	maxCommitTitleBytes = 72
	// maxDescriptionBytes is the most of a description the action sends;
	// GitLab accepts 1,048,576 characters.
	maxDescriptionBytes = 60000
	// maxDraftStrips bounds the loop that removes draft prefixes, as GitLab
	// bounds its own.
	maxDraftStrips = 20
	// fallbackTitle is the title of a merge request whose given title is
	// nothing but a draft prefix.
	fallbackTitle = "Update monitor definitions"
)

// draftPrefixPattern finds one draft prefix at the start of a title, with
// the space after it.
var draftPrefixPattern = regexp.MustCompile(`(?i)^(?:\[draft\]|\(draft\)|draft:)\s*`)

// referencePattern finds a reference that GitLab turns into a link and a
// note on the thing it points to: #12 and !34, with or without the project
// path before them, which may hold subgroups. The first group is what
// comes before the reference, which must not be a word character, a
// backtick (already a code span), or part of a path or another reference.
var referencePattern = regexp.MustCompile("(^|[^\\w`/.#!-])((?:(?:[A-Za-z0-9_.-]+/)+[A-Za-z0-9_.-]+)?[#!][0-9]+)\\b")

// closingTargetPattern finds what GitLab closes when a merge request merges
// into the default branch, besides an issue reference: a closing word
// (GitLab's own list, which has more words than GitHub's) followed by a URL
// or a Jira style key. The first group is the closing word and the space
// after it, the second is its target.
var closingTargetPattern = regexp.MustCompile("(?i)(\\b(?:clos(?:e[sd]?|ing)|fix(?:e[sd]|ing)?|resolv(?:e[sd]?|ing)|implement(?:s|ed|ing)?):?\\s+(?:issues?\\s+)?)(https?://[^\\s>`]+|[A-Z][A-Z0-9_]+-[0-9]+)")

// gitlabRules are the rules GitLab needs besides the ones of PlainText. The
// reference rule runs first, so that a line the quick action rule wraps in a
// code span is not wrapped again inside.
var gitlabRules = []gitchange.TextRule{referenceRule, quickActionRule}

// mergeRequestTitle returns the title of the merge request: on one line,
// with mentions, references and quick actions made harmless, and as a draft
// unless draft is false. A draft prefix the model wrote is removed first, so
// that draft: false really gives a merge request that is not a draft.
func mergeRequestTitle(raw string, draft bool) string {
	t := stripDraftPrefixes(gitchange.OneLine(raw))
	t = gitchange.OneLine(gitchange.PlainText(t, gitlabRules...))

	if t == "" {
		t = fallbackTitle
	}

	if draft {
		return draftMarker + t
	}

	return t
}

// commitTitle returns the first line of the commit message: the title on one
// line, made harmless like the title of the merge request.
func commitTitle(raw string) string {
	return source.CutText(gitchange.OneLine(gitchange.PlainText(raw, gitlabRules...)), maxCommitTitleBytes)
}

// descriptionText returns the text the model wrote for the description, made
// harmless and cut to the most the action sends.
func descriptionText(body string) string {
	return source.CutText(gitchange.MarkdownText(body, gitlabRules...), maxDescriptionBytes)
}

// stripDraftPrefixes removes the draft prefixes at the start of a title.
func stripDraftPrefixes(title string) string {
	for range maxDraftStrips {
		stripped := draftPrefixPattern.ReplaceAllString(title, "")
		if stripped == title {
			break
		}

		title = stripped
	}

	return strings.TrimSpace(title)
}

// referenceRule puts the references that GitLab links, and the targets that a
// closing word closes, in code spans, so the merge request writes a note on
// nothing and closes nothing.
func referenceRule(s string) string {
	s = referencePattern.ReplaceAllString(s, "$1`$2`")

	return closingTargetPattern.ReplaceAllString(s, "${1}`${2}`")
}

// quickActionRule makes sure no line of the text starts with a slash. GitLab
// runs a line that starts with a slash and a command name (/merge, /approve,
// /assign, /label, /close, /draft, /ready and the rest) as a quick action
// when it saves a description, with the rights of the token's user. It first
// deletes every carriage return, so that is done here too. A line that starts
// with a slash, after any spaces or tabs, is put in a code span, which GitLab
// does not read for commands.
func quickActionRule(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r", ""), "\n")

	for i, line := range lines {
		if trimmed := strings.TrimLeft(line, " \t"); strings.HasPrefix(trimmed, "/") {
			lines[i] = codeSpan(strings.TrimRight(trimmed, " \t"))
		}
	}

	return strings.Join(lines, "\n")
}

// codeSpan returns s as a Markdown code span. The fence is one backtick
// longer than the longest run of backticks in s, and s is padded with a space
// where it starts or ends with a backtick.
func codeSpan(s string) string {
	longest, run := 0, 0

	for _, r := range s {
		if r == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}

	fence := strings.Repeat("`", longest+1)

	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		return fence + " " + s + " " + fence
	}

	return fence + s + fence
}

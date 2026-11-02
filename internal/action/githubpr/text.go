package githubpr

import (
	"regexp"
	"strings"
)

// mentionPattern finds an @user or @org/team mention that GitHub would turn
// into a notification.
var mentionPattern = regexp.MustCompile("(^|[^\\w`@/.])@([A-Za-z0-9][A-Za-z0-9-]*(?:/[A-Za-z0-9_.-]+)?)")

// closingPattern finds a closing keyword followed by an issue reference,
// such as "fixes #12" or "closes acme/api#3", which closes the issue when
// the pull request merges into the default branch.
var closingPattern = regexp.MustCompile(`(?i)\b(close[sd]?|fix(?:e[sd])?|resolve[sd]?)(\s*:?\s+)((?:[\w.-]+/[\w.-]+)?#\d+|https?://\S+/issues/\d+)`)

// plainText turns text the model wrote into text for a pull request that
// notifies nobody and closes nothing: mentions and issue references that
// follow a closing keyword go into code spans, where GitHub does not act on
// them. The pull request is opened for a person to review, and the model
// wrote this text after reading alerts that an attacker may have shaped.
func plainText(s string) string {
	s = mentionPattern.ReplaceAllString(s, "$1`@$2`")

	return closingPattern.ReplaceAllString(s, "$1$2`$3`")
}

// markdownText is plainText for the description of a pull request, which
// GitHub renders as Markdown. It also stops the description from loading
// anything when the pull request is opened: an image, which GitHub fetches
// through its proxy as soon as someone views the page, would carry out
// whatever the model read, and so would raw HTML such as an img tag. An image
// shows as its text, and < is written as an HTML entity. A link stays, because
// a person has to click it.
func markdownText(s string) string {
	s = strings.ReplaceAll(plainText(s), "![", `!\[`)

	return strings.ReplaceAll(s, "<", "&lt;")
}

// oneLine returns s as a single line: runs of whitespace and control
// characters become one space.
func oneLine(s string) string {
	return strings.Join(strings.FieldsFunc(s, func(r rune) bool { return r < ' ' || r == 0x7f || r == ' ' }), " ")
}

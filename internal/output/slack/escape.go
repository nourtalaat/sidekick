package slack

import (
	"net/url"
	"strings"
)

// ellipsis ends text that was cut.
const ellipsis = "…"

// escaper escapes the three characters Slack's mrkdwn treats as control
// characters, so text cannot add mentions such as <!channel> or links.
var escaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// urlEscaper keeps a URL inside Slack's <url|text> syntax: the characters
// that would end it are percent-encoded, and & is escaped as in any text.
var urlEscaper = strings.NewReplacer("&", "&amp;", "<", "%3C", ">", "%3E", "|", "%7C", " ", "%20")

// escape returns s with Slack's control characters escaped. Every text
// that comes from an event or a report goes through it.
func escape(s string) string {
	return escaper.Replace(s)
}

// link returns a Slack link to rawURL with the text title, or "" when
// rawURL is not an absolute http or https URL.
func link(rawURL, title string) string {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return ""
	}

	return "<" + urlEscaper.Replace(u.String()) + "|" + escape(title) + ">"
}

// cut returns s with at most limit characters, ending with an ellipsis
// when it was longer. Surrounding white space is dropped.
func cut(s string, limit int) string {
	s = strings.TrimSpace(s)

	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}

	return strings.TrimSpace(string(runes[:limit-1])) + ellipsis
}

package linear

import (
	"regexp"
	"strings"

	"github.com/nourtalaat/sidekick/internal/action"
	"github.com/nourtalaat/sidekick/internal/weblink"
)

// markdownSpecial are the characters escaped with a backslash in the text
// of the model. Together they cover emphasis, code, links and images, raw
// HTML, headings, lists, quotes, tables, strikethrough, emoji codes,
// mentions, entities and the colon of a link, so the text renders as the
// text it is.
const markdownSpecial = "\\`*_{}[]()<>#+-=!|~:@&"

// orderedMarker is a line that Markdown would read as an item of a numbered
// list, such as "1. text".
var orderedMarker = regexp.MustCompile(`^(\s*\d+)\.`)

// urlEscaper keeps a URL inside the parentheses of a Markdown link.
var urlEscaper = strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E", "\\", "%5C")

// escape returns s with a backslash before every Markdown special
// character, and before the dot of a line that starts like a numbered list
// item. Carriage returns are dropped.
func escape(s string) string {
	s = strings.ReplaceAll(s, "\r", "")

	var sb strings.Builder

	sb.Grow(len(s))

	for _, r := range s {
		if strings.ContainsRune(markdownSpecial, r) {
			sb.WriteByte('\\')
		}

		sb.WriteRune(r)
	}

	lines := strings.Split(sb.String(), "\n")
	for i, line := range lines {
		lines[i] = orderedMarker.ReplaceAllString(line, `$1\.`)
	}

	return strings.Join(lines, "\n")
}

// withFooter returns the text of the model, escaped, followed by a rule and
// a line that says Sidekick wrote it and who approved it and links to the investigation when
// publicURL allows. The link is built here, never from the model's text.
func withFooter(text string, inv action.Invocation, publicURL string) string {
	footer := inv.ApprovalNote("Posted")
	if link := weblink.Investigation(publicURL, inv.InvestigationID); link != "" {
		footer += " [Investigation](" + urlEscaper.Replace(link) + ")"
	}

	return escape(text) + "\n\n---\n" + footer
}

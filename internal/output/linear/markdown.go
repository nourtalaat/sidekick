package linear

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/nourtalaat/sidekick/internal/output/ticket"
)

// markdownSpecial are the characters escaped with a backslash in every
// text. Together they cover emphasis, code, links and images, raw HTML,
// headings, lists, quotes, tables, strikethrough, emoji codes, mentions
// and entities, so untrusted text renders as the text it is.
const markdownSpecial = "\\`*_{}[]()<>#+-=!|~:@&"

// orderedMarker is a line that Markdown would read as an item of a
// numbered list, such as "1. text".
var orderedMarker = regexp.MustCompile(`^(\s*\d+)\.`)

// urlEscaper keeps a URL inside the parentheses of a Markdown link.
var urlEscaper = strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E", "\\", "%5C")

// render writes d as Linear Markdown. Every span's text is escaped; only a
// span's URL becomes a link.
func render(d ticket.Doc) string {
	parts := make([]string, 0, len(d.Blocks))

	for _, b := range d.Blocks {
		var sb strings.Builder

		if b.Heading != "" {
			sb.WriteString("**" + escape(b.Heading) + "**\n\n")
		}

		for i, line := range b.Lines {
			if i > 0 {
				sb.WriteString("\n")
			}

			switch b.Kind {
			case ticket.Bullets:
				sb.WriteString("- " + renderLine(line, true))
			case ticket.Numbered:
				sb.WriteString(strconv.Itoa(i+1) + ". " + renderLine(line, true))
			default:
				sb.WriteString(renderLine(line, false))
			}
		}

		parts = append(parts, sb.String())
	}

	return strings.Join(parts, "\n\n")
}

// renderLine writes the spans of one line. In a list item, line breaks in
// the text become spaces, so the text stays inside its item.
func renderLine(line ticket.Line, inList bool) string {
	var sb strings.Builder

	for _, s := range line {
		text := s.Text
		if inList {
			text = strings.ReplaceAll(text, "\n", " ")
		}

		sb.WriteString(renderSpan(s, text))
	}

	return sb.String()
}

// renderSpan writes one span: a link, or its text with its style. The
// markers go around the text without its outer spaces, because Markdown
// does not read "** text **" as bold.
func renderSpan(s ticket.Span, text string) string {
	if s.URL != "" {
		return "[" + escape(text) + "](" + urlEscaper.Replace(s.URL) + ")"
	}

	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (!s.Bold && !s.Italic) {
		return escape(text)
	}

	before := text[:len(text)-len(strings.TrimLeftFunc(text, unicode.IsSpace))]
	after := text[len(strings.TrimRightFunc(text, unicode.IsSpace)):]

	marked := escape(trimmed)
	if s.Italic {
		marked = "_" + marked + "_"
	}

	if s.Bold {
		marked = "**" + marked + "**"
	}

	return before + marked + after
}

// escape returns s with a backslash before every Markdown special
// character, and before the dot of a line that starts like a numbered
// list item. Carriage returns are dropped.
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

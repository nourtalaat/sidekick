package prompt

import (
	"html"
	"regexp"
	"strings"
	"unicode/utf8"
)

// untrustedTag names the element that marks untrusted data in a prompt.
const untrustedTag = "untrusted_data"

var (
	// untrustedTagPattern matches text that could be read as the start of
	// an untrusted_data tag, opening or closing: a "<" (or a look-alike),
	// an optional "/", and the tag name in any case. Whitespace and
	// invisible format characters may appear anywhere in between, and "-"
	// or a space may stand in for the "_".
	untrustedTagPattern = regexp.MustCompile(`(?i)[<\x{FF1C}\x{FE64}][\s\p{Cf}]*/?[\s\p{Cf}]*untrusted[\s\p{Cf}_-]*data`)

	// attributeEscaper escapes what html.EscapeString leaves that could
	// still make an attribute value look like more than one line.
	attributeEscaper = strings.NewReplacer("\n", "&#10;", "\r", "&#13;", "\t", "&#9;")
)

// WrapUntrusted marks content as untrusted data for the model:
//
//	<untrusted_data source="signoz" tool="signoz__search_logs" call="toolu_01">
//	...content...
//	</untrusted_data>
//
// source is the source or intake that produced the content, tool the
// qualified tool name and callID the tool call ID; empty ones are left
// out. Anything in content that could open or close an untrusted_data
// block, in any spelling, has its "<" replaced by "&lt;", so the content
// cannot end the block early or start a new one. Attribute values are
// escaped too. Every tool result and every piece of sender text goes
// through this before it reaches the model; content should already be
// redacted.
func WrapUntrusted(source, tool, callID, content string) string {
	var b strings.Builder

	b.WriteString("<" + untrustedTag)
	writeAttribute(&b, "source", source)
	writeAttribute(&b, "tool", tool)
	writeAttribute(&b, "call", callID)
	b.WriteString(">\n")

	escaped := escapeUntrusted(content)
	b.WriteString(escaped)

	if escaped != "" && !strings.HasSuffix(escaped, "\n") {
		b.WriteString("\n")
	}

	b.WriteString("</" + untrustedTag + ">")

	return b.String()
}

// writeAttribute writes ` name="value"` with value escaped, or nothing when
// value is empty.
func writeAttribute(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}

	b.WriteString(" " + name + `="` + attributeEscaper.Replace(html.EscapeString(value)) + `"`)
}

// escapeUntrusted replaces the "<" of every match of untrustedTagPattern
// with "&lt;".
func escapeUntrusted(content string) string {
	return untrustedTagPattern.ReplaceAllStringFunc(content, func(m string) string {
		_, size := utf8.DecodeRuneInString(m)

		return "&lt;" + m[size:]
	})
}

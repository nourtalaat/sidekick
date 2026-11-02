package jira

import (
	"strings"
	"unicode"
)

// adfVersion is the version of the Atlassian Document Format.
const adfVersion = 1

// node is one node of an Atlassian Document Format document.
type node struct {
	Type    string `json:"type"`
	Version int    `json:"version,omitempty"`
	Content []node `json:"content,omitempty"`
	Text    string `json:"text,omitempty"`
	Marks   []mark `json:"marks,omitempty"`
}

// mark styles a text node; here it only makes a link.
type mark struct {
	Type  string     `json:"type"`
	Attrs *linkAttrs `json:"attrs,omitempty"`
}

// linkAttrs are the attributes of a link mark.
type linkAttrs struct {
	Href string `json:"href"`
}

// document writes the text of the model as an Atlassian Document Format
// document, followed by the line note, which says Sidekick posted it and who
// approved it, with a link to the investigation when link is set, and the marker line when marker is
// set. The model's text only ever goes into text nodes, which Jira shows as
// plain text: a blank line starts a paragraph and a line break becomes a
// hard break. Only the footer's link is a link.
func document(body, note, link, marker string) node {
	doc := node{Type: "doc", Version: adfVersion, Content: []node{}}

	for _, para := range strings.Split(clean(body), "\n\n") {
		if strings.TrimSpace(para) == "" {
			continue
		}

		doc.Content = append(doc.Content, paragraph(para))
	}

	footer := node{Type: "paragraph", Content: []node{{Type: "text", Text: note}}}
	if link != "" {
		footer.Content = append(footer.Content,
			node{Type: "text", Text: " "},
			node{Type: "text", Text: "Investigation", Marks: []mark{{Type: "link", Attrs: &linkAttrs{Href: link}}}},
		)
	}

	doc.Content = append(doc.Content, footer)

	if marker != "" {
		doc.Content = append(doc.Content, node{Type: "paragraph", Content: []node{{Type: "text", Text: marker}}})
	}

	return doc
}

// paragraph returns a paragraph of the lines of text, with hard breaks
// between them. Empty lines are left out because a text node may not be
// empty.
func paragraph(text string) node {
	p := node{Type: "paragraph", Content: []node{}}

	for i, line := range strings.Split(text, "\n") {
		if i > 0 {
			p.Content = append(p.Content, node{Type: "hardBreak"})
		}

		if line != "" {
			p.Content = append(p.Content, node{Type: "text", Text: line})
		}
	}

	return p
}

// clean turns tabs into spaces and drops carriage returns and the other
// control characters, except the line feed, which Jira refuses in a text
// node.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r == '\n' || (r != '\r' && !unicode.IsControl(r)):
			return r
		default:
			return -1
		}
	}, s)
}

// oneLine returns s as a single line: runs of white space, including line
// breaks, become one space. The summary of an issue is one line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(clean(s)), " ")
}

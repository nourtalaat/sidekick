package jira

import (
	"strings"

	"github.com/nourtalaat/sidekick/internal/output/ticket"
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

// mark styles a text node.
type mark struct {
	Type  string     `json:"type"`
	Attrs *linkAttrs `json:"attrs,omitempty"`
}

// linkAttrs are the attributes of a link mark.
type linkAttrs struct {
	Href string `json:"href"`
}

// render writes d as an Atlassian Document Format document. Text always
// goes in text nodes, which Jira shows as plain text; only a span's URL
// becomes a link.
func render(d ticket.Doc) node {
	doc := node{Type: "doc", Version: adfVersion, Content: []node{}}

	for _, b := range d.Blocks {
		if b.Heading != "" {
			doc.Content = append(doc.Content, paragraph(ticket.Line{{Text: b.Heading, Bold: true}}))
		}

		switch b.Kind {
		case ticket.Bullets:
			doc.Content = append(doc.Content, list("bulletList", b.Lines))
		case ticket.Numbered:
			doc.Content = append(doc.Content, list("orderedList", b.Lines))
		default:
			for _, line := range b.Lines {
				doc.Content = append(doc.Content, paragraph(line))
			}
		}
	}

	return doc
}

// list returns a list of the given type with one item per line.
func list(listType string, lines []ticket.Line) node {
	items := make([]node, 0, len(lines))
	for _, line := range lines {
		items = append(items, node{Type: "listItem", Content: []node{paragraph(line)}})
	}

	return node{Type: listType, Content: items}
}

// paragraph returns a paragraph of the spans of line. Line breaks in a
// span become hard breaks, and empty text is left out, because a text
// node may not be empty.
func paragraph(line ticket.Line) node {
	p := node{Type: "paragraph", Content: []node{}}

	for _, s := range line {
		marks := marksOf(s)

		for i, part := range strings.Split(strings.ReplaceAll(s.Text, "\r", ""), "\n") {
			if i > 0 {
				p.Content = append(p.Content, node{Type: "hardBreak"})
			}

			if part != "" {
				p.Content = append(p.Content, node{Type: "text", Text: part, Marks: marks})
			}
		}
	}

	return p
}

// marksOf returns the marks of a span's style and link.
func marksOf(s ticket.Span) []mark {
	var marks []mark

	if s.Bold {
		marks = append(marks, mark{Type: "strong"})
	}

	if s.Italic {
		marks = append(marks, mark{Type: "em"})
	}

	if s.URL != "" {
		marks = append(marks, mark{Type: "link", Attrs: &linkAttrs{Href: s.URL}})
	}

	return marks
}

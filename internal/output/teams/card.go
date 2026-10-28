package teams

// The Adaptive Card version the sink writes; Teams renders up to 1.5.
const cardVersion = "1.4"

// The content type of an Adaptive Card attachment.
const contentTypeCard = "application/vnd.microsoft.card.adaptive"

// Adaptive Card element and action types.
const (
	typeCard      = "AdaptiveCard"
	typeContainer = "Container"
	typeRichText  = "RichTextBlock"
	typeTextRun   = "TextRun"
	typeOpenURL   = "Action.OpenUrl"
)

// Container styles, the colors of a card's sections. A style is always
// shown with an icon and a label too, never as the only sign.
const (
	styleAttention = "attention"
	styleGood      = "good"
	styleWarning   = "warning"
	styleAccent    = "accent"
	styleEmphasis  = "emphasis"
)

// Text run weights and sizes.
const (
	weightBolder = "Bolder"
	sizeMedium   = "Medium"
	spacingMed   = "Medium"
)

// card is an Adaptive Card. All text is in text runs of rich text blocks,
// which Teams shows as plain text: unlike a TextBlock, a text run reads no
// Markdown, so text from an alert or a model can add no links, mentions
// or formatting.
type card struct {
	Type    string     `json:"type"`
	Schema  string     `json:"$schema"`
	Version string     `json:"version"`
	MSTeams msTeams    `json:"msteams"`
	Body    []element  `json:"body"`
	Actions []cardLink `json:"actions,omitempty"`
}

// msTeams holds the Teams settings of a card.
type msTeams struct {
	Width string `json:"width"`
}

// element is a Container or a RichTextBlock.
type element struct {
	Type      string    `json:"type"`
	Style     string    `json:"style,omitempty"`
	Bleed     bool      `json:"bleed,omitempty"`
	Spacing   string    `json:"spacing,omitempty"`
	Separator bool      `json:"separator,omitempty"`
	Items     []element `json:"items,omitempty"`
	Inlines   []textRun `json:"inlines,omitempty"`
}

// textRun is a piece of plain text, which may open a link.
type textRun struct {
	Type         string    `json:"type"`
	Text         string    `json:"text"`
	Weight       string    `json:"weight,omitempty"`
	Size         string    `json:"size,omitempty"`
	IsSubtle     bool      `json:"isSubtle,omitempty"`
	Italic       bool      `json:"italic,omitempty"`
	SelectAction *cardLink `json:"selectAction,omitempty"`
}

// cardLink opens a URL.
type cardLink struct {
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
	URL   string `json:"url"`
}

// newCard returns a full-width card with body.
func newCard(body ...element) card {
	return card{
		Type:    typeCard,
		Schema:  "http://adaptivecards.io/schemas/adaptive-card.json",
		Version: cardVersion,
		MSTeams: msTeams{Width: "Full"},
		Body:    body,
	}
}

// container groups items in a section of style.
func container(style string, items ...element) element {
	return element{Type: typeContainer, Style: style, Bleed: true, Items: items}
}

// line is one line of text made of runs.
func line(runs ...textRun) element {
	return element{Type: typeRichText, Inlines: runs}
}

// plain is a run of text.
func plain(text string) textRun {
	return textRun{Type: typeTextRun, Text: text}
}

// bold is a run of bold text.
func bold(text string) textRun {
	return textRun{Type: typeTextRun, Text: text, Weight: weightBolder}
}

// subtle is a run of quieter text.
func subtle(text string) textRun {
	return textRun{Type: typeTextRun, Text: text, IsSubtle: true}
}

// linkRun is a run that opens rawURL, or plain text when rawURL is not an
// http or https URL.
func linkRun(text, rawURL string) textRun {
	r := plain(text)
	if isWebURL(rawURL) {
		r.SelectAction = &cardLink{Type: typeOpenURL, URL: rawURL}
	}

	return r
}
